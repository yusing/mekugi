package terminalview_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotWaitProgressMutedTargets(t *testing.T) {
	block := activityui.Block{Kind: "progress", Body: "Finished waiting for", WaitTargets: []activityui.WaitTarget{
		{Name: "/root/agent1", Status: "Still running"}, {Name: "/root/agent6", Status: "completed"},
	}}
	if block.ProgressText() != "Finished waiting for agent1: Still running, agent6: completed" {
		t.Fatal("plain summary differs")
	}
	var p activityui.Painter
	for _, width := range []int{18, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			rows := p.Block(block, width)
			rows = append(rows, "plain after wait")
			for _, row := range p.Block(block, width) {
				rows = append(rows, terminalui.FaintFallback(row))
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
	if strings.Contains(rows, "\x1b[31m") || strings.Contains(rows, "\x1b[2J") || ansi.Strip(rows) != "• Waiting agent: done" {
		t.Fatalf("unsafe wait: %q", rows)
	}
}

func TestUISnapshotWaitingBreathing(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
		t.Run(fmt.Sprint(theme), func(t *testing.T) {
			now := time.Unix(0, 0)
			p := activityui.Painter{Theme: theme, Clock: func() time.Time { return now }}
			if theme != diffrender.TerminalTheme {
				fg, bg := diffrender.RGB{R: 220, G: 220, B: 220}, diffrender.RGB{R: 20, G: 20, B: 20}
				if theme == diffrender.LightTheme {
					fg, bg = bg, fg
				}
				p.Colors = terminalui.Colors{Foreground: fg, Background: bg, HasForeground: true, HasBackground: true}
			}
			wait := activityui.Block{Kind: "progress", Body: "Waiting for", WaitTimeout: "10m0s", Live: true, WaitTargets: []activityui.WaitTarget{{Name: "/root/agent1"}, {Name: "/root/agent6"}}}
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

func TestUISnapshotWaitTimeoutMuted(t *testing.T) {
	p := activityui.Painter{Clock: func() time.Time { return time.Unix(0, 0) }}
	block := activityui.Block{Kind: "progress", Body: "Waiting for", Live: true, WaitTargets: []activityui.WaitTarget{{Name: "/root/worker"}}, WaitTimeout: "10s"}
	rows := append(p.Block(block, 100), "plain after wait")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/wait_timeout.txt", rows, 100)
}
