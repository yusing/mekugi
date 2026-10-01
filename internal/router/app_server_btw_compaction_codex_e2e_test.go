//go:build journal_e2e

package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Only the side answer reports high authoritative usage. Its next question
// therefore asks installed Codex to compact, without estimating tokens locally.
type btwCompactionProvider struct {
	mu                                sync.Mutex
	main                              string
	mainTurns, sideTurns, compactions int
}

func (p *btwCompactionProvider) forwardExecution(_, _ context.Context, _ []byte, headers http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	metadata, _ := decodeCodexTurnMetadata(headers)
	thread := headers.Get(threadIDHeader)
	if thread == "" {
		thread = metadata.ThreadID
	}
	if metadata.RequestKind == "compaction" {
		p.compactions++
		return nil, fmt.Errorf("unexpected provider compaction for %s", thread)
	}
	if p.main == "" {
		p.main = thread
	}
	tokens, text := 10, "Main remains available."
	if thread == p.main {
		p.mainTurns++
	} else {
		p.sideTurns++
		tokens, text = 150000, "First side answer complete."
	}
	id := fmt.Sprintf("btw-response-%d-%d", p.mainTurns, p.sideTurns)
	item := compactFixtureMessage(id+"-message", text)
	wire := routerFaultSSE(
		map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item},
		map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": tokens, "output_tokens": 1, "total_tokens": tokens + 1}}},
	)
	response := serverHTTPResponse(wire)
	response.Header.Set("Content-Type", "text/event-stream")
	return response, nil
}

func TestAppServerBTWCompactionNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	provider := &btwCompactionProvider{}
	var observationsMu sync.Mutex
	rejectedSideCompactions := 0
	handler := responsesHandler(t.Context(), time.Minute, provider, nil, proxy)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata, valid := decodeCodexTurnMetadata(r.Header)
		thread := r.Header.Get(threadIDHeader)
		if thread == "" {
			thread = metadata.ThreadID
		}
		if valid && metadata.RequestKind == "compaction" && proxy.isBTWThread(thread) {
			observationsMu.Lock()
			rejectedSideCompactions++
			observationsMu.Unlock()
		}
		handler(w, r)
	}))
	defer server.Close()
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	terminal := startAppResumeTerminalWithProxy(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.btw_fixture={name="btw_fixture",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="btw_fixture"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false", "-c", "model_auto_compact_token_limit=100000")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "", proxy)
	defer terminal.cancel()
	terminal.await("Ready")
	terminal.send("Seed Main context.\r")
	terminal.await("Main remains available.")
	terminal.await("completed")
	terminal.send("/btw first side question\r")
	terminal.await("First side answer complete.")
	terminal.awaitMatch("side completion", func(frame string) bool { return strings.Contains(frame, "/btw · completed") })
	terminal.send("/btw follow-up needing compaction\r")
	terminal.await("Side question requires compaction.")
	terminal.await("follow-up needing compaction")
	// Close the rejected panel, discard the restored side draft, and submit
	// another ordinary Main turn through the same installed host and router.
	terminal.send("\x1b\x03")
	terminal.send("Main after rejected side.\r")
	terminal.await("Main after rejected side.")
	terminal.await("completed")
	terminal.quit()
	observationsMu.Lock()
	rejected := rejectedSideCompactions
	observationsMu.Unlock()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	// Codex may retry its local compaction request. Every retry must remain
	// rejected, and none may start another side inference or reach upstream.
	if rejected < 1 || provider.compactions != 0 || provider.sideTurns != 1 || provider.mainTurns != 2 {
		t.Fatalf("compaction isolation: rejected=%d forwarded=%d side turns=%d Main turns=%d", rejected, provider.compactions, provider.sideTurns, provider.mainTurns)
	}
}
