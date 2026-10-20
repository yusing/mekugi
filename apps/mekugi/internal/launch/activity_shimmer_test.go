package launch

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestNativeUITerminalColorReports(t *testing.T) {
	u, _ := newAppServerTestUI()

	for _, key := range []byte("\x1b]10;rgb:dcdc/d6d6/c8c8\x1b\\\x1b]11;rgb:18/16/1e\a") {
		if err := terminalTest(u).key(key); err != nil {
			t.Fatal(err)
		}
	}
	want := terminalui.Colors{Foreground: diffrender.RGB{R: 220, G: 214, B: 200}, Background: diffrender.RGB{R: 24, G: 22, B: 30}, HasForeground: true, HasBackground: true}
	for _, view := range []*activityAdapter{u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents)} {
		if view.Painter.Colors != want || view.Painter.Theme != diffrender.DarkTheme {
			t.Fatalf("pane palette = %+v theme %v", view.Painter.Colors, view.Painter.Theme)
		}
	}
	if !u.shell.Agents.Painter.Colors.HasBackground || u.shell.Agents.Painter.Colors.Background != want.Background || terminalTest(u).Theme != diffrender.DarkTheme {
		t.Fatal("diff pane did not take the reported background")
	}
	if u.Text != "" {
		t.Fatalf("color reports leaked into the draft: %q", u.Text)
	}
}

func TestAppServerReasoningAnimationLifecycle(t *testing.T) {
	u, _ := newAppServerTestUI()

	v := u.testActivity(u.shell.Agents)
	v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/reviewer", Responding: true}}, Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "/root/reviewer", Kind: "reasoning", CallID: "r", Native: &sessionpresentation.NativeItem{Phase: "summary", Live: true}, Text: "Checking the answer target", Observed: time.Unix(0, 0)}}})
	first, _ := v.testCurrent(v.Agents[0], v.Entries[0].Source.Observed)
	second, _ := v.testCurrent(v.Agents[0], v.Entries[0].Source.Observed.Add(500*time.Millisecond))
	if !v.HasLiveStatus() || first == second || ansi.Strip(first) != ansi.Strip(second) {
		t.Fatal("live summary animation froze in roster or changed text")
	}
	before := v.Entries[0].Source.Observed
	v.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 2, Agent: "/root/reviewer", Kind: "reasoning", CallID: "r", Native: &sessionpresentation.NativeItem{Phase: "summary", Live: true}, Text: "Checking the answer target", Observed: time.Unix(1, 0)}}})
	if v.Entries[0].Source.Observed != before {
		t.Fatal("unchanged header restarted animation")
	}
	v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/reviewer"}}})
	if v.HasLiveStatus() || !strings.Contains(ansi.Strip(strings.Join(v.RenderFeed(80, 20).Lines, "\n")), "Checking the answer target") {
		t.Fatal("completed reasoning animation or transcript retention is wrong")
	}
}

func TestUISnapshotNativeStatusBreathing(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Unix(0, 0)
	u.clock = func() time.Time { return now }
	u.shell.Transcript.Clock, u.shell.Agents.Clock = u.clock, u.clock
	u.shell.Agents.Painter.Theme = diffrender.DarkTheme
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	waitTargetTestAgent(t, u, "child", "/root/reviewer", true)
	waitTargetTestEvent(t, u, "item/started", "main", "t", "w")
	item := binding.ThreadItem{ID: "r", Type: "reasoning", Summary: []string{"Checking results"}}
	reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "work", "item": item})
	var rows []string
	for _, state := range []string{"live", "completed"} {
		if state == "completed" {
			waitTargetTestEvent(t, u, "item/completed", "main", "t", "w")
			reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "work", "item": item})
		}
		if u.shell.Agents.HasLiveStatus() != (state == "live") {
			t.Fatalf("%s animation scheduling", state)
		}
		rows = append(rows, state)
		for _, phase := range []time.Duration{0, time.Second} {
			now = time.Unix(0, 0).Add(phase)
			rows = append(rows, u.testActivity(u.shell.Agents).NativeRoster(120, 6, now, true)...)
			rows = append(rows, "plain after roster")
		}
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/native-status-breathing.txt", rows, 120)
}
