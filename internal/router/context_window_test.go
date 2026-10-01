package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestAppServerContextWindow(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
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
	root := u.session.agent("/root")
	if root.ContextTokens != 100000 || root.InputTokens != 900000 {
		t.Fatalf("context conflated with cumulative usage: %+v", root)
	}
	u.model, u.reasoningEffort = "actual-model", "high"
	for _, width := range []int{12, 32, 80, 160} {
		frame, _ := u.mainFrame(width, 12, 0)
		for _, line := range frame {
			if ansi.StringWidth(line) > width {
				t.Fatalf("composer overflow at %d: %q", width, line)
			}
		}
		if width >= 32 && !strings.Contains(ansi.Strip(strings.Join(frame, "\n")), "100K/400K • 25%") {
			t.Fatalf("missing main context at %d: %v", width, frame)
		}
	}
	frame, _ := u.mainFrame(80, 12, 0)
	if bottom := ansi.Strip(frame[len(frame)-1]); !strings.HasSuffix(bottom, " actual-model (high) • 100K/400K • 25% ─╯") {
		t.Fatalf("context must follow model in composer: %q", bottom)
	}
	for _, width := range []int{40, 80, 160} {
		lines := u.agents.nativeRoster(width, 6, time.Now(), true)
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
	if got := contextWindowLabel(*root); got != "0/200K • 0%" {
		t.Fatal(got)
	}
	notify("main", 1000, 0)
	if got := contextWindowLabel(*root); got != "1K used" {
		t.Fatal(got)
	}
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "main", "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 1}}})
	if got := contextWindowLabel(*root); got != "0%" {
		t.Fatal(got)
	}
	u.session.start("fresh", t.TempDir())
	if got := contextWindowLabel(*u.session.agent("/root")); got != "0%" {
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
	u.requests["1"] = "thread/start"
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":1,"result":{"model":"model","thread":{"id":"main","path":%q}}}`, path))
	frame, _ := u.mainFrame(80, 8, 0)
	if bottom := ansi.Strip(frame[len(frame)-1]); !strings.Contains(bottom, "model • 100K/400K • 25%") {
		t.Fatal(bottom)
	}
	write("child", 20000)
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	child := u.session.agent("/root/worker")
	if child.ContextKnown {
		t.Fatal("child context arrived without evidence")
	}
	// thread/list may omit the rollout path; thread/read can supply it later.
	u.restoring = &appServerActivityRestore{order: []string{"child"}, listed: true}
	u.requests["2"] = "thread/read"
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":2,"result":{"thread":{"id":"child","path":%q,"turns":[]}}}`, path))
	if got := contextWindowLabel(*child); got != "20K/400K • 5%" {
		t.Fatal(got)
	}
	roster := ansi.Strip(strings.Join(u.agents.nativeRoster(160, 6, time.Now(), true), "\n"))
	if !strings.Contains(roster, "20K/400K • 5%") {
		t.Fatalf("saved child context did not reach roster: %s", roster)
	}
	// Metadata must never replace a newer live update with a stale disk snapshot.
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "child", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 40000}, "modelContextWindow": 400000}})
	u.session.registerThread(appServerThreadInfo{ID: "child", Path: path})
	if got := contextWindowLabel(*child); got != "40K/400K • 10%" {
		t.Fatal(got)
	}
	other := activityPaneAgent{}
	restoreContextUsage(&other, appServerThreadInfo{ID: "other", Path: path})
	if other.ContextKnown {
		t.Fatal("borrowed another thread's usage")
	}
	if err := os.WriteFile(path, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	restoreContextUsage(&other, appServerThreadInfo{ID: "child", Path: path})
	if other.ContextKnown {
		t.Fatal("malformed history became usage")
	}
}

func TestAppServerContextRolloutLatestCompleteRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	header := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"
	snapshot := func(tokens int) string {
		return fmt.Sprintf("{\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"total_tokens\":%d},\"model_context_window\":200000}}}\n", tokens)
	}
	// Oversized earlier content must not prevent bounded tail restoration.
	data := header + strings.Repeat("x", 9<<20) + "\n" + snapshot(90000) + snapshot(10000) + `{"type":"event_msg"`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	agent := activityPaneAgent{}
	restoreContextUsage(&agent, appServerThreadInfo{ID: "child", Path: path})
	if got := contextWindowLabel(agent); got != "10K/200K • 5%" {
		t.Fatal(got)
	}
}
