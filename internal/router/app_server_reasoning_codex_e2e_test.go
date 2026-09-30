//go:build journal_e2e

package router

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Gates keep the provider response open until the actual terminal shows each
// intermediate state. Completion cannot make a buffered stream appear to pass.
type appReasoningStreamProvider struct {
	stages [3]chan struct{}
}

func (p *appReasoningStreamProvider) forwardExecution(_, ctx context.Context, _ []byte, _ http.Header, _ string) (*http.Response, error) {
	reader, writer := io.Pipe()
	stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
	go func() {
		defer stop()
		defer writer.Close()
		emit := func(events ...any) bool {
			_, err := io.WriteString(writer, routerFaultSSE(events...))
			return err == nil
		}
		wait := func(stage int) bool {
			select {
			case <-p.stages[stage]:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !emit(
			map[string]any{"type": "response.created", "response": map[string]any{"id": "stream-response", "status": "in_progress", "output": []any{}}},
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "reason", "type": "reasoning", "summary": []any{}}},
		) || !wait(0) {
			return
		}
		if !emit(
			map[string]any{"type": "response.reasoning_summary_part.added", "item_id": "reason", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}},
			map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "reason", "output_index": 0, "summary_index": 0, "delta": "First public checkpoint."},
		) || !wait(1) {
			return
		}
		if !emit(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "reason", "output_index": 0, "summary_index": 0, "delta": "\nSecond public checkpoint."}) || !wait(2) {
			return
		}
		reason := map[string]any{"id": "reason", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "First public checkpoint.\nSecond public checkpoint."}}}
		answer := map[string]any{"id": "answer", "type": "message", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Reasoning acceptance complete.", "annotations": []any{}}}}
		emit(
			map[string]any{"type": "response.output_item.done", "output_index": 0, "item": reason},
			map[string]any{"type": "response.output_item.added", "output_index": 1, "item": answer},
			map[string]any{"type": "response.output_item.done", "output_index": 1, "item": answer},
			map[string]any{"type": "response.completed", "response": map[string]any{"id": "stream-response", "status": "completed", "output": []any{reason, answer}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}},
		)
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
}

func TestAppServerReasoningStreamNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	p := &appReasoningStreamProvider{stages: [3]chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, p, nil, nil))
	defer server.Close()
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	terminal := startAppResumeTerminal(t, func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}, "")
	// Cancel an unfinished provider before closing its HTTP server on failure.
	defer terminal.cancel()
	terminal.await("Ready")
	terminal.send("Exercise incremental public reasoning.\r")
	terminal.await("Working")
	if strings.Contains(terminal.screen.String(), "public checkpoint") {
		t.Fatal("summary appeared before the provider sent it")
	}
	// Leave measurable time before public text. Controlled-clock tests cover
	// exact start-time semantics independently of terminal delivery latency.
	select {
	case <-time.After(1100 * time.Millisecond):
	case <-terminal.ctx.Done():
		t.Fatal(terminal.ctx.Err())
	}
	close(p.stages[0])
	terminal.await("First public checkpoint.")
	if strings.Contains(terminal.screen.String(), "Second public checkpoint.") || strings.Contains(terminal.screen.String(), "First public checkpoint. for ") {
		t.Fatalf("first delta did not remain streaming:\n%s", terminal.screen.String())
	}
	close(p.stages[1])
	terminal.await("Second public checkpoint.")
	if !strings.Contains(terminal.screen.String(), "Thinking…") || strings.Contains(terminal.screen.String(), "Reasoning acceptance complete.") {
		t.Fatalf("second delta was not visible before completion:\n%s", terminal.screen.String())
	}
	close(p.stages[2])
	terminal.await("Reasoning acceptance complete.")
	terminal.await("Thought for ")
	terminal.awaitMatch("folded reasoning", func(frame string) bool {
		return strings.Contains(frame, "Thought for ") && !strings.Contains(frame, "Second public checkpoint.")
	})
	terminal.quit()
}
