package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/contracts/inference"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestAppServerContextWindow(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	setTestAppServerIntegration(u, &observationFixture{usage: newTestUsage(t)})
	u.usage.Observation("main", "main", "gpt-6-sol", "").Observe(inference.Usage{InputTokens: 900000, UncachedInputTokens: 900000, OutputTokens: 50000})
	notify := func(thread string, used, window uint64) {
		appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{
			"threadId": thread, "tokenUsage": map[string]any{
				"total": map[string]any{"inputTokens": 900000, "outputTokens": 50000},
				"last":  map[string]any{"totalTokens": used}, "modelContextWindow": window,
			},
		})
	}
	notify("main", 100000, 400000)
	notify("child", 20000, 200000)
	root := u.attached.session.Agent("/root")
	if root.ContextTokens != 100000 || root.InputTokens != 900000 {
		t.Fatalf("context conflated with cumulative usage: %+v", root)
	}
	u.settings.Model, u.settings.Effort = "actual-model", "high"
	for _, width := range []int{12, 32, 80, 160} {
		frame, _ := u.testMainFrame(width, 12, 0)
		for _, line := range frame {
			if ansi.StringWidth(line) > width {
				t.Fatalf("composer overflow at %d: %q", width, line)
			}
		}
		caption, _ := u.testStatLines(t, width, 12)
		if width >= 32 && !strings.Contains(caption, "25%") || width >= 160 && !strings.Contains(caption, " 25% 100K/400K") {
			t.Fatalf("missing main context at %d: %v", width, frame)
		}
	}
	if bottom, _ := u.testStatLines(t, 160, 12); !strings.HasPrefix(bottom, "  actual-model (high) | ") || !strings.Contains(bottom, " 25% 100K/400K") {
		t.Fatalf("composer caption must show the model, then context use: %q", bottom)
	}
	for _, width := range []int{40, 80, 160} {
		lines := u.testActivity(u.shell.Agents).RosterSheet(width, 6, time.Now())
		for _, line := range lines {
			if ansi.StringWidth(line) > width {
				t.Fatalf("roster overflow at %d: %q", width, line)
			}
		}
		roster := ansi.Strip(strings.Join(lines, "\n"))
		if width >= 80 && !strings.Contains(roster, "20K/200K • 10%") {
			t.Fatalf("missing thread-isolated context at %d: %s", width, roster)
		}
	}
	notify("main", 10000, 400000)
	if root.ContextTokens != 10000 {
		t.Fatalf("compaction did not reduce context: %+v", root)
	}
	notify("main", 0, 200000)
	if got := activityui.ContextWindowLabel(testActivityAgent(*root)); got != "0/200K • 0%" {
		t.Fatal(got)
	}
	notify("main", 1000, 0)
	if got := activityui.ContextWindowLabel(testActivityAgent(*root)); got != "1K used" {
		t.Fatal(got)
	}
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "main", "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 1}}})
	if got := activityui.ContextWindowLabel(testActivityAgent(*root)); got != "0%" {
		t.Fatal(got)
	}
	u.startSession("fresh", t.TempDir())
	if got := activityui.ContextWindowLabel(testActivityAgent(*u.attached.session.Agent("/root"))); got != "0%" {
		t.Fatal(got)
	}
}

func TestAppServerStartupRestoresContext(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	write := func(id string, used int) {
		t.Helper()
		data := fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"total_tokens\":%d},\"total_token_usage\":{\"total_tokens\":999999},\"model_context_window\":400000}}}\n", id, used)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	u.Startup.Thread = "main"
	appServerTestRequest(t, u, "1", "thread/resume")
	appServerTestMessage(t, u, `{"id":1,"result":{"model":"model","thread":{"id":"main","contextTokens":100000,"contextWindow":400000}}}`)
	if bottom, _ := u.testStatLines(t, 80, 12); !strings.HasPrefix(bottom, "  model | ") || !strings.Contains(bottom, " 25% 100K/400K") {
		t.Fatal(bottom)
	}
	write("child", 20000)
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	child := u.attached.session.Agent("/root/worker")
	if child.ContextKnown {
		t.Fatal("child context arrived without evidence")
	}
	restoreContextUsage(child, binding.ThreadInfo{ID: "child", Path: path})
	applyActivity(&u.attached, &u.approvals, u.review, &u.pendingEditReceipts, u.Thread, u.clock, nil, u.attached.session.Roster())
	if got := activityui.ContextWindowLabel(testActivityAgent(*child)); got != "20K/400K • 5%" {
		t.Fatal(got)
	}
	roster := ansi.Strip(strings.Join(u.testActivity(u.shell.Agents).RosterSheet(160, 6, time.Now()), "\n"))
	if !strings.Contains(roster, "20K/400K • 5%") {
		t.Fatalf("saved child context did not reach roster: %s", roster)
	}
	// Metadata must never replace a newer live update with a stale snapshot.
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "child", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 40000}, "modelContextWindow": 400000}})
	registerThread(&u.attached.session, binding.ThreadInfo{ID: "child", ContextTokens: new(uint64(20000)), ContextWindow: new(uint64(400000))})
	if got := activityui.ContextWindowLabel(testActivityAgent(*child)); got != "40K/400K • 10%" {
		t.Fatal(got)
	}
	other := sessionpresentation.ActivityAgent{}
	restoreContextUsage(&other, binding.ThreadInfo{ID: "other", Path: path})
	if other.ContextKnown {
		t.Fatal("borrowed another thread's usage")
	}
	if err := os.WriteFile(path, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	restoreContextUsage(&other, binding.ThreadInfo{ID: "child", Path: path})
	if other.ContextKnown {
		t.Fatal("malformed history became usage")
	}
}

func TestAppServerRateLimitsReachCaption(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	caption := func() string {
		line, _ := u.testStatLines(t, 160, 12)
		return line
	}
	if strings.Contains(caption(), "5h:") {
		t.Fatal("usage shown before Codex reported it")
	}
	window := func(used float64, minutes int) map[string]any {
		return map[string]any{"usedPercent": used, "windowDurationMins": minutes}
	}
	appServerTestNotify(t, u, "account/rateLimits/updated", map[string]any{"rateLimits": map[string]any{"limitId": "codex", "primary": window(12, 300), "secondary": window(21, 10080)}})
	if got := caption(); !strings.Contains(got, "5h: 12% Weekly: 21%") {
		t.Fatalf("usage windows missing: %q", got)
	}
	// Sparse updates keep absent windows; other limits leave the default alone.
	appServerTestNotify(t, u, "account/rateLimits/updated", map[string]any{"rateLimits": map[string]any{"primary": window(40, 300)}})
	appServerTestNotify(t, u, "account/rateLimits/updated", map[string]any{"rateLimits": map[string]any{"limitId": "other", "primary": window(90, 300)}})
	if got := caption(); !strings.Contains(got, "5h: 40% Weekly: 21%") {
		t.Fatalf("sparse or foreign update misapplied: %q", got)
	}
}
