package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestNativeUITerminalColorReports(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	for _, key := range []byte("\x1b]10;rgb:dcdc/d6d6/c8c8\x1b\\\x1b]11;rgb:18/16/1e\a") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	want := activityui.Colors{Foreground: livediff.RGB{R: 220, G: 214, B: 200}, Background: livediff.RGB{R: 24, G: 22, B: 30}, HasForeground: true, HasBackground: true}
	for _, view := range []*liveActivityView{u.view, u.agents} {
		if view.painter.Colors != want || view.painter.Theme != livediff.DarkTheme {
			t.Fatalf("pane palette = %+v theme %v", view.painter.Colors, view.painter.Theme)
		}
	}
	if !u.shell.diff.backgrounded || u.shell.diff.background != want.Background || u.shell.diff.theme != livediff.DarkTheme {
		t.Fatal("diff pane did not take the reported background")
	}
	if u.draft != "" {
		t.Fatalf("color reports leaked into the draft: %q", u.draft)
	}
}

func TestAppServerReasoningAnimationLifecycle(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	v := u.agents
	v.apply(activityPaneEvent{Kind: "entries", Agents: []activityPaneAgent{{Name: "/root/reviewer", Responding: true}}, Entries: []activityPaneEntry{{Seq: 1, Agent: "/root/reviewer", Kind: "reasoning", CallID: "r", Text: "Checking the answer target", Observed: time.Now().Add(-500 * time.Millisecond)}}})
	first, _ := v.current(v.agents[0], v.entries[0].Observed)
	second, _ := v.current(v.agents[0], v.entries[0].Observed.Add(500*time.Millisecond))
	if !v.hasLiveReasoning() || first == second || ansi.Strip(first) != ansi.Strip(second) {
		t.Fatal("live summary animation froze in roster or changed text")
	}
	before := v.entries[0].Observed
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 2, Agent: "/root/reviewer", Kind: "reasoning", CallID: "r", Text: "Checking the answer target", Observed: time.Now()}}})
	if v.entries[0].Observed != before {
		t.Fatal("unchanged header restarted animation")
	}
	v.agents[0].Responding = false
	if v.hasLiveReasoning() || !strings.Contains(ansi.Strip(strings.Join(v.renderFeed(80, 20).lines, "\n")), "Checking the answer target") {
		t.Fatal("completed reasoning animation or transcript retention is wrong")
	}
}
