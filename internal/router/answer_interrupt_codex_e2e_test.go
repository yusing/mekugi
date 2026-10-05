//go:build journal_e2e

package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
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

	"github.com/coder/websocket"
)

// Hold actual input deltas, rather than holding provider admission or a
// completed tool result. The installed Codex still executes every host tool.
type answerInterruptCodexProvider struct {
	t           *testing.T
	mu          sync.Mutex
	turn        int
	mode, input string
	gate        chan struct{}
	requests    chan []byte
}

func (p *answerInterruptCodexProvider) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(responsesRequestBufferBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	messages := readResponsesWebSocket(ctx, conn, cancel)
	for {
		var message webSocketMessage
		select {
		case message = <-messages:
		case <-ctx.Done():
			return
		}
		if webSocketMessageReadError(ctx, message) != nil {
			return
		}
		response, err := p.forwardExecution(ctx, ctx, message.body, r.Header, "")
		if err != nil {
			if ctx.Err() == nil {
				p.t.Error(err)
			}
			return
		}
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if payload, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				if err := conn.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
					response.Body.Close()
					return
				}
			}
		}
		response.Body.Close()
		if scanner.Err() != nil {
			return
		}
	}
}

func (p *answerInterruptCodexProvider) forwardExecution(_, ctx context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	p.turn++
	turn := p.turn
	p.mu.Unlock()
	select {
	case p.requests <- bytes.Clone(body):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if turn == 1 {
		question := &questionCodexProvider{mode: "async", requests: make(chan []byte, 1)}
		return question.forwardExecution(ctx, ctx, body, nil, "")
	}
	if turn == 2 {
		reader, writer := io.Pipe()
		stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
		go func() {
			defer stop()
			defer writer.Close()
			item := map[string]any{"type": "custom_tool_call", "id": "answer-tool-item", "call_id": "answer-tool", "name": p.mode, "status": "in_progress", "input": ""}
			emit := func(events ...any) bool {
				_, err := io.WriteString(writer, routerFaultSSE(events...))
				return err == nil
			}
			split := len(p.input) / 2
			if !emit(
				map[string]any{"type": "response.created", "response": map[string]any{"id": "answer-emission", "status": "in_progress", "output": []any{}}},
				map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item},
				map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": item["id"], "output_index": 0, "delta": p.input[:split]},
			) {
				return
			}
			select {
			case <-p.gate:
			case <-ctx.Done():
				return
			}
			if !emit(map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": item["id"], "output_index": 0, "delta": p.input[split:]}) {
				return
			}
			item["input"], item["status"] = p.input, "completed"
			emit(
				map[string]any{"type": "response.custom_tool_call_input.done", "item_id": item["id"], "output_index": 0, "input": p.input},
				map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item},
				map[string]any{"type": "response.completed", "response": map[string]any{"id": "answer-emission", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}},
			)
		}()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	}
	if turn == 3 && p.mode == "exec" {
		var request struct {
			Input []struct {
				Type   string         `json:"type"`
				CallID string         `json:"call_id"`
				Output jsontext.Value `json:"output"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		for _, item := range request.Input {
			if item.Type == "custom_tool_call_output" && item.CallID == "answer-tool" {
				_, _, _, cell := stockPatchResultState("exec", []byte(item.Output))
				if cell != "" {
					return mchangesNestedCodexResponse(turn, map[string]any{"type": "function_call", "id": "answer-wait-item", "call_id": "answer-wait", "name": "wait", "status": "completed", "arguments": string(mustMarshalJSON(map[string]any{"cell_id": cell, "yield_time_ms": 10000}))}), nil
				}
			}
		}
		return nil, fmt.Errorf("answer did not yield the running Code Mode cell: %.2000s", body)
	}
	if turn == 4 && p.mode == "exec" {
		var request struct {
			Input []struct {
				Type   string         `json:"type"`
				CallID string         `json:"call_id"`
				Output jsontext.Value `json:"output"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		completed := false
		for _, item := range request.Input {
			if item.Type == "function_call_output" && item.CallID == "answer-wait" {
				output := strings.Join(executionOutputTexts([]byte(item.Output)), "\n")
				completed = strings.Contains(output, "input-survived") && strings.Contains(output, "Script completed")
			}
		}
		if !completed {
			return nil, fmt.Errorf("wait did not complete the original Code Mode cell")
		}
	}
	if turn == 3 || turn == 4 && p.mode == "exec" {
		return mchangesNestedCodexResponse(turn, instantInterruptAnswer("Answer emission acceptance complete")), nil
	}
	return nil, fmt.Errorf("unexpected provider request %d", turn)
}

type answerInterruptResponseWriter struct {
	http.ResponseWriter
	emitted chan struct{}
}

func (w *answerInterruptResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *answerInterruptResponseWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	if err == nil && bytes.Contains(body[:n], []byte(`"type":"response.custom_tool_call_input.delta"`)) {
		select {
		case w.emitted <- struct{}{}:
		default:
		}
	}
	return n, err
}

func TestAnswerInterruptNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required for answer interruption acceptance")
	}
	for _, mode := range []string{"apply_patch", "exec"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			workspace := t.TempDir()
			gatePath := filepath.Join(workspace, "release")
			startedPath := filepath.Join(workspace, "started")
			countPath := filepath.Join(workspace, "count")
			patchPath := filepath.Join(workspace, "answer-patch.txt")
			provider := &answerInterruptCodexProvider{t: t, mode: mode, gate: make(chan struct{}), requests: make(chan []byte, 8)}
			if mode == "apply_patch" {
				provider.input = "*** Begin Patch\n*** Add File: " + patchPath + "\n+input-survived\n*** End Patch"
			} else {
				shell := "echo run >> " + strconv.Quote(countPath) + "; touch " + strconv.Quote(startedPath) + "; while test ! -f " + strconv.Quote(gatePath) + "; do sleep 0.1; done; echo input-survived"
				provider.input = "// @exec: {\"yield_time_ms\": 60000}\nconst result = await tools.exec_command({cmd: " + string(mustMarshalJSON(shell)) + ", workdir: " + string(mustMarshalJSON(workspace)) + ", yield_time_ms: 30000}); text(result.output);"
			}
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			issues := NewCriticalErrors()
			emitted := make(chan struct{}, 2)
			upstream := httptest.NewServer(http.HandlerFunc(provider.serve))
			defer upstream.Close()
			client := newProviderClient(upstream.URL, upstream.Client())
			client.enableWebSockets(t.Context())
			defer client.websockets.close()
			handler := responsesHandler(t.Context(), time.Minute, client, issues, proxy)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for key, values := range codexAuthHeaders() {
					r.Header[key] = values
				}
				handler(&answerInterruptResponseWriter{ResponseWriter: w, emitted: emitted}, r)
			}))
			defer server.Close()
			gateClosed := false
			defer func() {
				if !gateClosed {
					close(provider.gate)
				}
				_ = os.WriteFile(gatePath, nil, 0o600)
			}()
			environment := routerFaultCodexEnvironment(t)
			terminal := startAppResumeTerminalWithProxy(t, func(ctx context.Context) *exec.Cmd {
				cmd := exec.CommandContext(ctx, codex, "app-server", "--enable", "instant_interrupt", "-c", `model_providers.answer_fixture={name="OpenAI",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="answer_fixture"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.code_mode=true", "-c", "features.code_mode_host=true", "-c", "include_collaboration_mode_instructions=false", "-c", "features.default_mode_request_user_input=true")
				cmd.Env, cmd.Dir = environment, workspace
				return cmd
			}, "", proxy)
			defer terminal.cancel()
			next := func() []byte {
				t.Helper()
				select {
				case request := <-provider.requests:
					return request
				case <-terminal.ctx.Done():
					t.Fatalf("provider request missing: notices=%v\n%s", issues.Pending(), terminal.screen.String())
					return nil
				}
			}
			terminal.await("Ready")
			terminal.send("Ask the release question, then perform held work\r")
			_ = next()
			terminal.await("Who should receive the update?")
			_ = next()
			// This signal comes from the consumer-facing HTTP writer, after both
			// response.created and output_item.added, not from provider admission.
			select {
			case <-emitted:
			case <-terminal.ctx.Done():
				t.Fatal("tool input delta was not delivered")
			}
			terminal.send("\x02q")
			terminal.await("1 of 1")
			terminal.send("1\r")
			terminal.awaitMatch("accepted queued answer", func(screen string) bool {
				queued := strings.Contains(screen, "Steering after") && strings.Contains(screen, "Working") && !strings.Contains(screen, "Sending…")
				// Let a broken early-send path reach the request assertion below.
				answered := strings.Contains(screen, "Who should receive the update?") && strings.Contains(screen, "Customers") && !strings.Contains(screen, "1 of 1")
				return queued || answered
			})
			select {
			case <-provider.requests:
				t.Fatalf("answer interrupted unfinished %s input", mode)
			case <-time.After(300 * time.Millisecond):
			case <-terminal.ctx.Done():
				t.Fatal(terminal.ctx.Err())
			}
			close(provider.gate)
			gateClosed = true
			request := next()
			var parsed struct {
				Input []any `json:"input"`
			}
			if err := json.Unmarshal(request, &parsed); err != nil {
				t.Fatal(err)
			}
			input, err := json.Marshal(parsed.Input)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(input), "<send_user_message_question_reply>") != 1 || !bytes.Contains(input, []byte("Customers")) {
				t.Fatal("answer was not delivered exactly once after tool input completed")
			}
			if mode == "exec" {
				if _, err := os.Stat(startedPath); err != nil {
					t.Fatalf("host command did not start: %v", err)
				}
				if _, err := os.Stat(gatePath); !os.IsNotExist(err) {
					t.Fatalf("answer waited for command gate: %v", err)
				}
				if err := os.WriteFile(gatePath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				_ = next()
				count, err := os.ReadFile(countPath)
				if err != nil || string(count) != "run\n" {
					t.Fatalf("host command execution count %q, %v", count, err)
				}
			} else {
				data, err := os.ReadFile(patchPath)
				if err != nil || string(data) != "input-survived\n" {
					t.Fatalf("streamed patch did not execute intact: %q, %v", data, err)
				}
			}
			terminal.await("Answer emission acceptance complete")
			terminal.await("completed")
			terminal.quit()
			if pending := issues.Pending(); len(pending) != 0 {
				t.Fatalf("answer interruption produced router failures: %v", pending)
			}
		})
	}
}
