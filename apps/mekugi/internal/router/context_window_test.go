package router

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
	root := u.session.Agent("/root")
	if root.ContextTokens != 100000 || root.InputTokens != 900000 {
		t.Fatalf("context conflated with cumulative usage: %+v", root)
	}
	u.model, u.reasoningEffort = "actual-model", "high"
	for _, width := range []int{12, 32, 80, 160} {
		frame, _ := u.testMainFrame(width, 12, 0)
		for _, line := range frame {
			if ansi.StringWidth(line) > width {
				t.Fatalf("composer overflow at %d: %q", width, line)
			}
		}
		if width >= 32 && !strings.Contains(ansi.Strip(strings.Join(frame, "\n")), "100K/400K • 25%") {
			t.Fatalf("missing main context at %d: %v", width, frame)
		}
	}
	frame, _ := u.testMainFrame(80, 12, 0)
	if bottom := ansi.Strip(frame[len(frame)-1]); !strings.HasSuffix(bottom, " actual-model (high) • 100K/400K • 25% ─╯") {
		t.Fatalf("context must follow model in composer: %q", bottom)
	}
	for _, width := range []int{40, 80, 160} {
		lines := u.testActivity(u.agents).NativeRoster(width, 6, time.Now(), true)
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
	if got := activityui.ContextWindowLabel(testActivityAgent(*u.session.Agent("/root"))); got != "0%" {
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
	write("main", 100000)
	appServerTestRequest(t, u, "1", "thread/start")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":1,"result":{"model":"model","thread":{"id":"main","path":%q}}}`, path))
	frame, _ := u.testMainFrame(80, 8, 0)
	if bottom := ansi.Strip(frame[len(frame)-1]); !strings.Contains(bottom, "model • 100K/400K • 25%") {
		t.Fatal(bottom)
	}
	write("child", 20000)
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	child := u.session.Agent("/root/worker")
	if child.ContextKnown {
		t.Fatal("child context arrived without evidence")
	}
	// thread/list may omit the rollout path; thread/read can supply it later.
	u.restoring = &appServerActivityRestore{state: restoredDiscoveryTestState("main", "child")}
	appServerTestRequest(t, u, "2", "thread/read")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":2,"result":{"thread":{"id":"child","path":%q,"turns":[]}}}`, path))
	if got := activityui.ContextWindowLabel(testActivityAgent(*child)); got != "20K/400K • 5%" {
		t.Fatal(got)
	}
	roster := ansi.Strip(strings.Join(u.testActivity(u.agents).NativeRoster(160, 6, time.Now(), true), "\n"))
	if !strings.Contains(roster, "20K/400K • 5%") {
		t.Fatalf("saved child context did not reach roster: %s", roster)
	}
	// Metadata must never replace a newer live update with a stale disk snapshot.
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "child", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 40000}, "modelContextWindow": 400000}})
	u.registerThread(binding.ThreadInfo{ID: "child", Path: path})
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
