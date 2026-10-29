package terminalview_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/presentation"

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
	if strings.Contains(rows, "\x1b[31m") || strings.Contains(rows, "\x1b[2J") || ansi.Strip(rows) != "↔ Waiting agent: done" {
		t.Fatalf("unsafe wait: %q", rows)
	}
}

func TestCompletedWaitReasonNotRepeated(t *testing.T) {
	block := activityui.Block{Kind: "progress", Body: "Finished waiting for", WaitTargets: []activityui.WaitTarget{{Name: "/root/review", Status: "completed"}}, WaitReason: "completed"}
	want := "Finished waiting for review: completed"
	if got := block.ProgressText(); got != want {
		t.Fatalf("plain progress=%q", got)
	}
}

func TestUISnapshotAgentDisplayNames(t *testing.T) {
	var p activityui.Painter
	rows := []string{p.Agent("agent:main"), p.Agent("agent:main/worker/reviewer")}
	rows = append(rows, p.Block(activityui.Block{Kind: "progress", Body: "Waiting for", WaitTargets: []activityui.WaitTarget{{Name: "agent:main/worker/reviewer"}}}, 80)...)
	canonical := []string{p.Agent("/root"), p.Agent("/root/worker/reviewer")}
	canonical = append(canonical, p.Block(activityui.Block{Kind: "progress", Body: "Waiting for", WaitTargets: []activityui.WaitTarget{{Name: "/root/worker/reviewer"}}}, 80)...)
	if strings.Join(rows, "\n") != strings.Join(canonical, "\n") {
		t.Fatalf("coordination address changed terminal labels or colours: %q != %q", rows, canonical)
	}
	uisnapshot.Assert(t, "testdata/snapshots/agent_display_names.txt", strings.Join(rows, "\n"))
}

func TestUISnapshotWaitTargetDisclosure(t *testing.T) {
	block := activityui.Block{Kind: "progress", Body: "Waiting for"}
	for _, name := range []string{"one", "two", "three", "four", "five"} {
		block.WaitTargets = append(block.WaitTargets, activityui.WaitTarget{Name: "/root/" + name})
	}
	var view activityui.View
	if !view.ClickTarget(&block, activityui.Snippet{}, 80) {
		t.Fatal("omitted targets have no detail interaction")
	}
	page := view.Painter.DialogPage(block, 80)
	if page.Text != "Waiting for one, two, three, four, five" {
		t.Fatalf("target details lost full selection: %q", page.Text)
	}
	for _, width := range []int{28, 100} {
		rows := view.Painter.Block(block, width)
		if text := strings.Join(strings.Fields(ansi.Strip(strings.Join(rows, "\n"))), " "); !strings.Contains(text, "+3 more") || strings.Contains(text, "three") {
			t.Fatalf("wait preview did not collapse: %s", text)
		}
		rows = append(rows, "plain after wait")
		rows = append(rows, view.Painter.Block(activityui.Block{Kind: "progress", Body: "Waiting for", WaitTargets: []activityui.WaitTarget{{Name: "all"}}}, width)...)
		uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/wait_disclosure_%d.txt", width), rows, width)
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

func TestUISnapshotWaitingRosterMatchesTranscript(t *testing.T) {
	now := time.Unix(0, 0)
	v := activityui.View{Painter: activityui.Painter{Clock: func() time.Time { return now }}}
	var rows []string
	for _, timeout := range []string{"", "10s"} {
		block := activityui.Block{Kind: "progress", Body: "Waiting for", Live: true,
			WaitTargets: []activityui.WaitTarget{{Name: "/root/backend-extraction/backend-extraction-review"}}, WaitTimeout: timeout}
		agent := activitymodel.Agent{Waiting: true, Wait: &block.Value}
		got := v.AgentState(agent, 120, now)
		if want := v.Painter.Summary([]activityui.Block{block}, 120); got != want {
			t.Fatalf("roster %q differs from transcript summary %q", got, want)
		}
		rows = append(rows, v.Painter.Block(block, 120)...)
		rows = append(rows, got, "plain after wait")
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/wait_roster.txt", rows, 120)
}
