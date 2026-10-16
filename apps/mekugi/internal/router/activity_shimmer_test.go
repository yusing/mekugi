package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestNativeUITerminalColorReports(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	for _, key := range []byte("\x1b]10;rgb:dcdc/d6d6/c8c8\x1b\\\x1b]11;rgb:18/16/1e\a") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	want := terminalui.Colors{Foreground: diffrender.RGB{R: 220, G: 214, B: 200}, Background: diffrender.RGB{R: 24, G: 22, B: 30}, HasForeground: true, HasBackground: true}
	for _, view := range []*liveActivityView{u.view, u.agents} {
		if view.painter.Colors != want || view.painter.Theme != diffrender.DarkTheme {
			t.Fatalf("pane palette = %+v theme %v", view.painter.Colors, view.painter.Theme)
		}
	}
	if !u.shell.diff.backgrounded || u.shell.diff.background != want.Background || u.shell.diff.theme != diffrender.DarkTheme {
		t.Fatal("diff pane did not take the reported background")
	}
	if u.Text != "" {
		t.Fatalf("color reports leaked into the draft: %q", u.Text)
	}
}

func TestAppServerReasoningAnimationLifecycle(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	v := u.agents
	v.apply(activityPaneEvent{Kind: "entries", Agents: []activityPaneAgent{{Name: "/root/reviewer", Responding: true}}, Entries: []activityPaneEntry{{Seq: 1, Agent: "/root/reviewer", Kind: "reasoning", CallID: "r", native: &liveActivityNativeItem{phase: "summary", live: true}, Text: "Checking the answer target", Observed: time.Unix(0, 0)}}})
	first, _ := v.current(v.Agents[0], v.Entries[0].Source.Observed)
	second, _ := v.current(v.Agents[0], v.Entries[0].Source.Observed.Add(500*time.Millisecond))
	if !v.hasLiveStatus() || first == second || ansi.Strip(first) != ansi.Strip(second) {
		t.Fatal("live summary animation froze in roster or changed text")
	}
	before := v.Entries[0].Source.Observed
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 2, Agent: "/root/reviewer", Kind: "reasoning", CallID: "r", native: &liveActivityNativeItem{phase: "summary", live: true}, Text: "Checking the answer target", Observed: time.Unix(1, 0)}}})
	if v.Entries[0].Source.Observed != before {
		t.Fatal("unchanged header restarted animation")
	}
	v.Agents[0].Responding = false
	if v.hasLiveStatus() || !strings.Contains(ansi.Strip(strings.Join(v.renderFeed(80, 20).lines, "\n")), "Checking the answer target") {
		t.Fatal("completed reasoning animation or transcript retention is wrong")
	}
}

func TestUISnapshotNativeStatusBreathing(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	now := time.Unix(0, 0)
	u.clock = func() time.Time { return now }
	u.view.clock, u.agents.clock = u.clock, u.clock
	u.agents.painter.Theme = diffrender.DarkTheme
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
	waitTargetTestAgent(t, u, "child", "/root/reviewer", true)
	waitTargetTestEvent(t, u, "item/started", "main", "t", "w")
	item := appServerItem{ID: "r", Type: "reasoning", Summary: []string{"Checking results"}}
	reasoningTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "work", "item": item})
	var rows []string
	for _, state := range []string{"live", "completed"} {
		if state == "completed" {
			waitTargetTestEvent(t, u, "item/completed", "main", "t", "w")
			reasoningTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "work", "item": item})
		}
		if u.agents.hasLiveStatus() != (state == "live") {
			t.Fatalf("%s animation scheduling", state)
		}
		rows = append(rows, state)
		for _, phase := range []time.Duration{0, time.Second} {
			now = time.Unix(0, 0).Add(phase)
			rows = append(rows, u.agents.nativeRoster(120, 6, now, true)...)
			rows = append(rows, "plain after roster")
		}
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/native-status-breathing.txt", rows, 120)
}
