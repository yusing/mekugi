//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi"
)

func init() {
	if os.Getenv("MEKUGI_COMPACT_TEST_WORKER") == "1" && len(os.Args) == 2 && os.Args[1] == "post-compact" {
		os.Exit(RunPostCompactHook(context.Background(), nil, os.Stdin, os.Stdout, os.Stderr))
	}
}

type compactCodexProvider struct {
	mu                                sync.Mutex
	store                             *mekugiReplayStore
	workspace                         string
	turns                             int
	compacted, restored, existingHook bool
}

func (p *compactCodexProvider) forwardExecution(ctx, _ context.Context, body []byte, headers http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns++
	metadata, _ := decodeCodexTurnMetadata(headers)
	thread := metadata.ThreadID
	if thread == "" {
		thread = headers.Get(threadIDHeader)
	}
	var item map[string]any
	tokens := 10
	switch p.turns {
	case 1:
		if thread == "" {
			return nil, fmt.Errorf("native request missing stable thread")
		}
		ctx, release, err := p.store.beginSession(ctx, thread, "")
		if err != nil {
			return nil, err
		}
		defer release()
		store := p.store.scoped(ctx)
		journal := newJournalStore()
		if err := journal.initialize(ctx, store, p.workspace, thread, "/root", ""); err != nil {
			return nil, err
		}
		if err := journal.bindIdentity(ctx, store, p.workspace, thread, "", "/root", true); err != nil {
			return nil, err
		}
		if _, err := journal.apply(ctx, store, p.workspace, thread, "fixture-journal", []journalMutation{{Op: "add", Text: new("Durable native recovery milestone")}}); err != nil {
			return nil, err
		}
		id, err := store.reserveChange(ctx, p.workspace, thread, "fixture-edit")
		if err != nil {
			return nil, err
		}
		if err := store.put(ctx, p.workspace, map[string]mekugiHistory{"fixture-edit": {
			ChangeID: id, CorrelationID: "fixture-edit", ExecutingThread: thread, Applied: true,
			ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("recovered.txt", "recovered.txt", "old\n", "new\n")},
		}}); err != nil {
			return nil, err
		}
		item = map[string]any{"type": "custom_tool_call", "id": "compact-exec", "call_id": "compact-exec-call", "name": "exec", "status": "completed", "input": `text("continue after compaction");`}
		tokens = 150000
	case 2:
		if metadata.RequestKind != "compaction" {
			return nil, fmt.Errorf("expected native compaction, got %q", metadata.RequestKind)
		}
		p.compacted = true
		item = compactFixtureMessage("compacted", "Continue the task after compaction.")
	case 3:
		var request struct {
			Input []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		for _, input := range request.Input {
			if input.Role != "developer" {
				continue
			}
			for _, part := range input.Content {
				if part.Text == "existing compact hook" {
					p.existingHook = true
				}
				if strings.Contains(part.Text, "Durable native recovery milestone") && strings.Contains(part.Text, "1\t1\trecovered.txt") {
					p.restored = true
				}
			}
		}
		if !p.restored {
			return nil, fmt.Errorf("immediate continuation lacks durable hook context")
		}
		item = compactFixtureMessage("done", "native recovery accepted")
	default:
		return nil, fmt.Errorf("unexpected extra request %d", p.turns)
	}
	id := fmt.Sprintf("compact-response-%d", p.turns)
	response := map[string]any{"id": id, "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": tokens, "output_tokens": 1, "total_tokens": tokens + 1}}
	var wire strings.Builder
	for _, event := range []any{
		map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item},
		map[string]any{"type": "response.completed", "response": response},
	} {
		data, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&wire, "data: %s\n\n", data)
	}
	result := serverHTTPResponse(wire.String())
	result.Header.Set("Content-Type", "text/event-stream")
	return result, nil
}

func compactFixtureMessage(id, text string) map[string]any {
	return map[string]any{"type": "message", "id": id, "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func TestPostCompactNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	codexDirectory := t.TempDir()
	t.Setenv("CODEX_HOME", codexDirectory)
	// File-based hooks must coexist with the wrapper's invocation-local hook.
	if err := os.WriteFile(filepath.Join(codexDirectory, "hooks.json"), []byte(`{"hooks":{"SessionStart":[{"matcher":"^compact$","hooks":[{"type":"command","command":"printf 'existing compact hook'"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("MEKUGI_COMPACT_TEST_WORKER", "1")
	directory, err := defaultMekugiReplayDirectory()
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	provider := &compactCodexProvider{store: store, workspace: workspace}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil, nil))
	defer server.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hook := "'" + strings.ReplaceAll(filepath.Clean(executable), "'", "'\\''") + "' post-compact"
	config := `model_providers.compact_fixture={name="compact_fixture",base_url=` + strconv.Quote(server.URL+"/v1") + `,wire_api="responses",requires_openai_auth=false}`
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	// Only this isolated, fully controlled fixture bypasses trust, which also
	// runs the file-based hook. Production pre-trusts only its own session hook.
	command := exec.CommandContext(ctx, codex,
		"exec", "--skip-git-repo-check", "--json", "--color", "never",
		"--dangerously-bypass-hook-trust", "-c", config, "-c", `model_provider="compact_fixture"`,
		"-c", "features.plugins=false", "-c", "model_auto_compact_token_limit=100000",
		"-c", fmt.Sprintf(`hooks.SessionStart=[{matcher="^compact$",hooks=[{type="command",command=%q,timeout=5}]}]`, hook),
		"--model", "gpt-6-astra", "--sandbox", "danger-full-access", "-C", workspace,
		"Exercise native post-compaction recovery.")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("native compaction: %v\nstdout: %s\nstderr: %s", err, &stdout, &stderr)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.turns != 3 || !provider.compacted || !provider.restored || !provider.existingHook || !strings.Contains(stdout.String(), "native recovery accepted") {
		t.Fatalf("native compaction turns=%d compacted=%t restored=%t existingHook=%t\nstdout: %s\nstderr: %s", provider.turns, provider.compacted, provider.restored, provider.existingHook, &stdout, &stderr)
	}
}
