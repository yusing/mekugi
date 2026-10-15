package activity_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/testdata/uisnapshot"
)

func TestMutedAgentPalette(t *testing.T) {
	for _, tc := range []struct {
		name        string
		normal, dim int
	}{
		{"/root/agent6", 39, 31}, {"/root/agent1", 170, 133}, {"/root/agent5", 38, 30},
		{"/root/agent0", 99, 61}, {"/root/agent3", 37, 29}, {"/root/agent7", 133, 96}, {"/root/agent2", 74, 67},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen := vt.NewEmulator(4, 1)
			defer screen.Close()
			fmt.Fprint(screen, activityui.FaintFallback(activityui.Color(tc.name)+"N"+activityui.Reset+activityui.DimColor(tc.name)+"D"+activityui.Reset+"X"))
			expected := vt.NewEmulator(4, 1)
			defer expected.Close()
			fmt.Fprintf(expected, "\x1b[1;38;5;%dmN\x1b[0;38;5;%dmD\x1b[0mX", tc.normal, tc.dim)
			for x := range 3 {
				if !screen.CellAt(x, 0).Style.Equal(&expected.CellAt(x, 0).Style) {
					t.Fatalf("cell %d style = %+v, want %+v", x, screen.CellAt(x, 0).Style, expected.CellAt(x, 0).Style)
				}
			}
		})
	}
}

func TestUISnapshotWaitProgressMutedTargets(t *testing.T) {
	block := activityui.Block{Kind: "progress", Body: "Finished waiting", WaitTargets: []activityui.WaitTarget{
		{Name: "/root/agent1", Status: "Still running"}, {Name: "/root/agent6", Status: "completed"},
	}}
	if block.ProgressText() != "Finished waiting · agent1: Still running, agent6: completed" {
		t.Fatal("plain summary differs")
	}
	var p activityui.Painter
	for _, width := range []int{18, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			rows := p.Block(block, width)
			rows = append(rows, "plain after wait")
			for _, row := range p.Block(block, width) {
				rows = append(rows, activityui.FaintFallback(row))
			}
			rows = append(rows, "plain after wait")
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/wait_targets_%d.txt", width), rows, width)
		})
	}
}

func TestWaitProgressSanitizesHostText(t *testing.T) {
	b := activityui.Block{Kind: "progress", Body: "Waiting", WaitTargets: []activityui.WaitTarget{{Name: "/root/agent\x1b[31m", Status: "done\x1b[2J"}}}
	p := activityui.Painter{}
	rows := strings.Join(p.Block(b, 120), "\n")
	if strings.Contains(rows, "\x1b[31m") || strings.Contains(rows, "\x1b[2J") || ansi.Strip(rows) != "• Waiting · agent: done" {
		t.Fatalf("unsafe wait: %q", rows)
	}
}

func TestUISnapshotWaitingBreathing(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.TerminalTheme, livediff.DarkTheme, livediff.LightTheme} {
		t.Run(fmt.Sprint(theme), func(t *testing.T) {
			now := time.Unix(0, 0)
			p := activityui.Painter{Theme: theme, Clock: func() time.Time { return now }}
			if theme != livediff.TerminalTheme {
				fg, bg := livediff.RGB{R: 220, G: 220, B: 220}, livediff.RGB{R: 20, G: 20, B: 20}
				if theme == livediff.LightTheme {
					fg, bg = bg, fg
				}
				p.Colors = activityui.Colors{Foreground: fg, Background: bg, HasForeground: true, HasBackground: true}
			}
			wait := activityui.Block{Kind: "progress", Body: "Waiting for agent (timeout 600s)", Live: true, WaitTargets: []activityui.WaitTarget{{Name: "/root/agent1"}, {Name: "/root/agent6"}}}
			reasoning := activityui.Block{Kind: "summary", Body: "Checking results", Live: true}
			var rows []string
			for _, phase := range []time.Duration{0, time.Second} {
				now = time.Unix(0, 0).Add(phase)
				rows = append(rows, fmt.Sprint(phase))
				rows = append(rows, p.Block(wait, 44)...)
				rows = append(rows, p.Summary([]activityui.Block{wait}, 100), p.Summary([]activityui.Block{reasoning}, 100))
				rows = append(rows, "plain after status")
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/wait_breathing_%d.txt", theme), rows, 100)
		})
	}
}
