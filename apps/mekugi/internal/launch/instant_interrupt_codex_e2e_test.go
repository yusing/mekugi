//go:build codex_e2e

package launch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/yusing/mekugi/backends/codex/binding"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

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

// The installed Codex, not the router, owns both preemption and the live Code
// Mode interpreter. This provider holds only response bytes, never host tools.
type instantInterruptCodexProvider struct {
	t        *testing.T
	mu       sync.Mutex
	mode     string
	program  string
	gate     chan struct{}
	requests chan []byte
	turn     int
	cell     string
}

// Mock providers keep receiving peer cancellation while their response is gated.
func readTestProviderMessages(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc) <-chan []byte {
	messages := make(chan []byte, 1)
	go func() {
		defer close(messages)
		for {
			_, body, err := conn.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			select {
			case messages <- body:
			case <-ctx.Done():
				return
			}
		}
	}()
	return messages
}

func (p *instantInterruptCodexProvider) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(codexresponses.MaxRequestBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// Keep detecting peer cancellation while the model response is gated.
	messages := readTestProviderMessages(ctx, conn, cancel)
	for {
		var message []byte
		select {
		case message = <-messages:
		case <-ctx.Done():
			return
		}
		if len(message) == 0 {
			return
		}
		response, err := p.ForwardExecution(ctx, ctx, message, r.Header, "")
		if err != nil {
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

func (p *instantInterruptCodexProvider) ForwardExecution(_ context.Context, responseCtx context.Context, body []byte, _ http.Header, _ string) (_ *inferencegateway.Response, resultErr error) {
	defer func() {
		if resultErr != nil {
			p.t.Error(resultErr)
		}
	}()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turn++
	select {
	case p.requests <- bytes.Clone(body):
	case <-responseCtx.Done():
		return nil, responseCtx.Err()
	default:
		return nil, fmt.Errorf("unexpected provider request overflow at %d", p.turn)
	}
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
	switch p.turn {
	case 1:
		if p.mode == "model" {
			reader, writer := io.Pipe()
			response := routerFaultCodexStreamResponse("")
			response.Body = reader
			go func() {
				defer writer.Close()
				_, _ = io.WriteString(writer, "data: "+string(responses.MustMarshalJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": "instant-held", "status": "in_progress", "output": []any{}}}))+"\n\n")
				select {
				case <-p.gate:
					_, _ = io.WriteString(writer, routerFaultSSE(map[string]any{"type": "response.completed", "response": map[string]any{"id": "instant-held", "status": "completed", "output": []any{}}}))
				case <-responseCtx.Done():
					_ = writer.CloseWithError(responseCtx.Err())
				}
			}()
			return testGatewayResponse(response, nil)
		}
		return testGatewayResponse(mchangesNestedCodexResponse(1, map[string]any{"type": "custom_tool_call", "id": "instant-exec-item", "call_id": "instant-exec", "name": "exec", "status": "completed", "input": p.program}), nil)
	case 2:
		if !bytes.Contains(body, []byte("Steer now")) {
			return nil, fmt.Errorf("steering input missing from replacement request: %.1000s", body)
		}
		if p.mode == "model" {
			return testGatewayResponse(mchangesNestedCodexResponse(2, instantInterruptAnswer("Steered while model response was gated")), nil)
		}
		for _, item := range request.Input {
			if item.Type == "custom_tool_call_output" && item.CallID == "instant-exec" {
				p.cell = strings.TrimPrefix(binding.DecodeExecutionResult("exec", []byte(item.Output)).ResumeHandle, "cell:")
			}
		}
		if p.cell == "" {
			return nil, fmt.Errorf("steering did not yield the original exec cell: %.2000s", body)
		}
		return testGatewayResponse(mchangesNestedCodexResponse(2, map[string]any{"type": "function_call", "id": "instant-wait-item", "call_id": "instant-wait", "name": "wait", "status": "completed", "arguments": string(responses.MustMarshalJSON(map[string]any{"cell_id": p.cell, "yield_time_ms": 10000}))}), nil)
	case 3:
		for _, item := range request.Input {
			if item.Type == "function_call_output" && item.CallID == "instant-wait" {
				output := strings.Join(binding.ExecutionOutputTexts([]byte(item.Output)), "\n")
				if !strings.Contains(output, "cell-survived") || !strings.Contains(output, "Script completed") {
					return nil, fmt.Errorf("wait did not complete the original cell: %s", output)
				}
				return testGatewayResponse(mchangesNestedCodexResponse(3, instantInterruptAnswer("Steered cell completed")), nil)
			}
		}
		return nil, fmt.Errorf("wait result missing")
	default:
		return nil, fmt.Errorf("unexpected provider request %d", p.turn)
	}
}

func instantInterruptAnswer(answer string) map[string]any {
	return map[string]any{"type": "message", "id": "instant-answer", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}
}

type instantInterruptResponseWriter struct {
	http.ResponseWriter
	created chan struct{}
}

func (w *instantInterruptResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *instantInterruptResponseWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	if err == nil && bytes.Contains(body[:n], []byte(`"type":"response.created"`)) {
		select {
		case w.created <- struct{}{}:
		default:
		}
	}
	return n, err
}

func TestInstantInterruptNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required for instant_interrupt acceptance")
	}
	for _, mode := range []string{"model", "code"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			workspace := t.TempDir()
			gatePath := filepath.Join(workspace, "release")
			startedPath := filepath.Join(workspace, "started")
			countPath := filepath.Join(workspace, "count")
			provider := &instantInterruptCodexProvider{t: t, mode: mode, gate: make(chan struct{}), requests: make(chan []byte, 4)}
			shell := "echo run >> " + strconv.Quote(countPath) + "; touch " + strconv.Quote(startedPath) + "; while test ! -f " + strconv.Quote(gatePath) + "; do sleep 0.1; done; echo cell-survived"
			provider.program = "// @exec: {\"yield_time_ms\": 60000}\nconst result = await tools.exec_command({cmd: " + string(responses.MustMarshalJSON(shell)) + ", workdir: " + string(responses.MustMarshalJSON(workspace)) + ", yield_time_ms: 30000}); text(result.output);"
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			upstream := httptest.NewServer(http.HandlerFunc(provider.serve))
			defer upstream.Close()
			client := newTestProviderClient(upstream.URL, upstream.Client())
			client.OpenAI.EnableWebSockets(t.Context())
			defer client.OpenAI.Close()
			issues := testCriticalErrors()
			created := make(chan struct{}, 1)
			handler := testResponsesHandler(t.Context(), time.Minute, client, issues, proxy)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Fixture credentials terminate at the local mock provider.
				for key, values := range codexAuthHeaders() {
					r.Header[key] = values
				}
				handler(&instantInterruptResponseWriter{ResponseWriter: w, created: created}, r)
			}))
			defer server.Close()
			defer close(provider.gate)
			environment := routerFaultCodexEnvironment(t)
			command := func(ctx context.Context) *exec.Cmd {
				cmd := exec.CommandContext(ctx, codex, "app-server", "--enable", "instant_interrupt", "-c", `model_providers.instant_fixture={name="OpenAI",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="instant_fixture"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.code_mode=true", "-c", "features.code_mode_host=true", "-c", "include_collaboration_mode_instructions=false")
				cmd.Env, cmd.Dir = environment, workspace
				return cmd
			}
			terminal := startAppResumeTerminalWithIntegration(t, command, "", testAppServerIntegration(proxy))
			terminal.await("Ready")
			terminal.send("Start held work\r")
			select {
			case <-provider.requests:
			case <-terminal.ctx.Done():
				t.Fatalf("first provider request missing: notices=%v\n%s", issues.Notices.Pending(), terminal.screen.String())
			}
			// Steer only after response copying has delivered the first event,
			// rather than racing provider admission or startup inspection.
			select {
			case <-created:
			case <-terminal.ctx.Done():
				t.Fatalf("response.created was not delivered: %v", issues.Notices.Pending())
			}
			if mode == "code" {
				deadline := time.After(10 * time.Second)
				for {
					if _, err := os.Stat(startedPath); err == nil {
						break
					}
					select {
					case <-deadline:
						t.Fatal("nested host command did not start")
					case <-time.After(20 * time.Millisecond):
					}
				}
			}
			terminal.send("Steer now\r")
			select {
			case <-provider.requests:
				if _, err := os.Stat(gatePath); err == nil {
					t.Fatal("gate opened before replacement request")
				}
			case <-terminal.ctx.Done():
				t.Fatalf("steer did not reach provider before gate opened: %v", terminal.ctx.Err())
			}
			if mode == "code" {
				if err := os.WriteFile(gatePath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				select {
				case <-provider.requests:
				case <-terminal.ctx.Done():
					t.Fatal("wait continuation request missing")
				}
				count, err := os.ReadFile(countPath)
				if err != nil || string(count) != "run\n" {
					t.Fatalf("host tool executed %q, %v; want exactly one run", count, err)
				}
				terminal.await("Steered cell completed")
			} else {
				terminal.await("Steered while model response was gated")
			}
			terminal.await("completed")
			terminal.quit()
			if pending := issues.Notices.Pending(); len(pending) != 0 {
				t.Fatalf("instant interruption produced router failures: %v", pending)
			}
		})
	}
}
