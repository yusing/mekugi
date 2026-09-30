//go:build journal_e2e

package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

// Installed host acceptance and repeatable payload measurement. The only
// migration is of a synthetic fixture in an isolated Codex home. No provider
// requests, accounts, user histories or installed binaries are changed.
func TestAppServerAnchoredHistoryNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	environment := routerFaultCodexEnvironment(t)
	var codexHome string
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			codexHome = value
		}
	}
	workspace := t.TempDir()
	const thread = "019f9b56-5a00-7000-8000-000000000001"
	const turn = "019f9b56-5a00-7000-8000-000000000002"
	const stamp = "2026-09-30T00:00:00Z"
	path := filepath.Join(codexHome, "sessions", "2026", "09", "30", "rollout-2026-09-30T00-00-00-"+thread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := jsontext.NewEncoder(file)
	write := func(kind string, payload any) {
		t.Helper()
		if err := json.MarshalEncode(encoder, map[string]any{"timestamp": stamp, "type": kind, "payload": payload}); err != nil {
			t.Fatal(err)
		}
	}
	write("session_meta", map[string]any{"id": thread, "session_id": thread, "timestamp": stamp, "cwd": workspace, "originator": "codex", "cli_version": "0.159.2", "source": "cli", "model_provider": "openai", "history_mode": "legacy"})
	write("event_msg", map[string]any{"type": "task_started", "turn_id": turn, "started_at": 100, "model_context_window": 100000, "collaboration_mode_kind": "default"})
	write("response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Measure restoration."}}})
	write("event_msg", map[string]any{"type": "user_message", "message": "Measure restoration.", "kind": "plain", "local_images": []any{}, "text_elements": []any{}})
	for i := range 2000 {
		text := fmt.Sprintf("item %04d %s", i, strings.Repeat("x", 4096))
		write("response_item", map[string]any{"type": "message", "id": fmt.Sprintf("item-%04d", i), "role": "assistant", "phase": "commentary", "content": []any{map[string]any{"type": "output_text", "text": text}}})
		write("event_msg", map[string]any{"type": "item_completed", "thread_id": thread, "turn_id": turn, "started_at_ms": 100000 + i, "completed_at_ms": 101000 + i, "item": map[string]any{"type": "AgentMessage", "id": fmt.Sprintf("item-%04d", i), "content": []any{map[string]any{"type": "Text", "text": text}}, "phase": "commentary"}})
	}
	write("event_msg", map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "done", "started_at": 100, "completed_at": 110, "duration_ms": 10000})
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	start := func() *appserver.Client {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), codex, "app-server", "-c", "features.plugins=false")
		cmd.Env = environment
		cmd.Dir = workspace
		client, err := appserver.Start(cmd)
		if err != nil {
			t.Fatal(err)
		}
		id, err := client.Initialize()
		if err != nil {
			t.Fatal(err)
		}
		awaitHistoryRPC(t, client, id)
		if _, err := client.Send("initialized", map[string]any{}, false); err != nil {
			t.Fatal(err)
		}
		return client
	}
	client := start()
	// The host admits metadata from the fixture before its own migration.
	id, err := client.Send("thread/read", map[string]any{"threadId": thread, "includeTurns": false}, true)
	if err != nil {
		t.Fatal(err)
	}
	awaitHistoryRPC(t, client, id)
	if err := client.Shutdown(); err != nil {
		t.Fatal(err)
	}
	migration := exec.CommandContext(t.Context(), codex, "migrate-rollouts", "--apply", "--thread", thread, "--json")
	migration.Env = environment
	output, err := migration.Output()
	if err != nil {
		t.Fatalf("isolated host migration: %v", err)
	}
	var migrated struct {
		Outcomes []struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal(output, &migrated); err != nil || len(migrated.Outcomes) != 1 || migrated.Outcomes[0].Status != "migrated" {
		t.Fatalf("migration did not complete: %s", output)
	}
	client = start()
	t.Cleanup(client.Close)
	started := time.Now()
	id, err = client.Send("thread/read", map[string]any{"threadId": thread, "includeTurns": true}, true)
	if err != nil {
		t.Fatal(err)
	}
	full := awaitHistoryRPC(t, client, id)
	fullDuration := time.Since(started)
	var read struct {
		Thread appServerThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(full.Result, &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Thread.Turns) != 1 || len(read.Thread.Turns[0].Items) != 2001 || read.Thread.HistoryMode != "paginated" {
		t.Fatalf("measurement fixture not materialized: turns=%d items=%d mode=%s", len(read.Thread.Turns), len(read.Thread.Turns[0].Items), read.Thread.HistoryMode)
	}
	baseline, _ := newAppServerTestUI()
	baseline.agents = newLiveActivityView()
	baseline.ctx = t.Context()
	baseline.ensureShell()
	t.Cleanup(func() { baseline.shell.diff.close(); baseline.shell.diffScreen.Close() })
	baseline.session.start("main", workspace)
	baseline.session.registerThread(appServerThreadInfo{ID: thread, AgentNickname: "worker", AgentRole: "worker"})
	projectionStarted := time.Now()
	baseline.restoreActivityThread(read.Thread)
	fullDuration += time.Since(projectionStarted)
	read.Thread.Turns = nil
	u, _ := newAppServerTestUI()
	u.client = client
	u.ctx = t.Context()
	u.agents = newLiveActivityView()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	u.session.start("main", workspace)
	u.resumeThread = "main"
	u.session.registerThread(appServerThreadInfo{ID: thread, AgentNickname: "worker", AgentRole: "worker"})
	u.restoring = &appServerActivityRestore{root: appServerThreadInfo{ID: "main", Cwd: workspace}, order: []string{thread}, listed: true, pane: make(map[string][]*restoredPlacement), itemAt: make(map[string]map[string]time.Time)}
	started = time.Now()
	if err := u.startChildHistory(read.Thread); err != nil {
		t.Fatal(err)
	}
	pageBytes := drainHistoryLoader(t, u)
	pageDuration := time.Since(started)
	if u.restoring != nil || len(u.agents.entries) != 100 || pageBytes*10 >= len(full.Result) {
		t.Fatalf("unbounded consumer hydration: entries=%d page=%d full=%d", len(u.agents.entries), pageBytes, len(full.Result))
	}
	if !strings.HasPrefix(u.agents.entries[0].Text, "item 1900 ") || !strings.HasPrefix(u.agents.entries[99].Text, "item 1999 ") {
		t.Fatal("host descending pages not rendered chronologically")
	}
	if u.session.agent("/root/worker").Started.Unix() != 100 || u.session.agent("/root/worker").LastResponse.Unix() != 110 {
		t.Fatal("host turn timing lost")
	}
	if err := u.loadOlderActivity(); err != nil {
		t.Fatal(err)
	}
	drainHistoryLoader(t, u)
	if len(u.agents.entries) != 200 || !strings.HasPrefix(u.agents.entries[0].Text, "item 1800 ") {
		t.Fatal("installed anchor page boundary lost or duplicated")
	}
	// Fork history is host-visible lineage, never an unscoped parent read.
	id, err = client.Send("thread/fork", map[string]any{"threadId": thread, "excludeTurns": true}, true)
	if err != nil {
		t.Fatal(err)
	}
	fork := awaitHistoryRPC(t, client, id)
	var forked struct {
		Thread appServerThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(fork.Result, &forked); err != nil {
		t.Fatal(err)
	}
	id, err = client.Send("thread/items/list", map[string]any{"threadId": forked.Thread.ID, "turnId": turn, "sortDirection": "desc", "limit": 100, "cursor": map[string]any{"type": "item", "itemId": "item-1900"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	inherited := awaitHistoryRPC(t, client, id)
	var items struct {
		Data []struct {
			Item appServerItem `json:"item"`
		} `json:"data"`
	}
	if err := json.Unmarshal(inherited.Result, &items); err != nil {
		t.Fatal(err)
	}
	if len(items.Data) != 100 || items.Data[0].Item.ID != "item-1899" || items.Data[99].Item.ID != "item-1800" {
		t.Fatal("fork anchor ignored visible inherited history")
	}
	t.Logf("synthetic 2000-message restoration: full result + projection %d bytes / %s; metadata + recent100 %d bytes / %s (latency diagnostic, no timing threshold)", len(full.Result), fullDuration, pageBytes, pageDuration)
}

func awaitHistoryRPC(t *testing.T, client *appserver.Client, id string) appserver.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		select {
		case m, ok := <-client.Messages:
			if !ok {
				t.Fatal("host closed during history read")
			}
			if string(m.ID) == id {
				if m.Error != nil {
					t.Fatalf("host history RPC failed: %+v", m.Error)
				}
				return m
			}
		case <-ctx.Done():
			t.Fatal("host history RPC timed out")
		}
	}
}

func drainHistoryLoader(t *testing.T, u *appServerUI) int {
	t.Helper()
	total := 0
	for u.historyLoading != nil {
		m := awaitHistoryRPC(t, u.client, u.historyLoading.requestID)
		total += len(m.Result)
		if err := u.message(m); err != nil {
			t.Fatal(err)
		}
	}
	return total
}
