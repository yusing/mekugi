//go:build journal_e2e

package router

import (
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

func (p *instantInterruptCodexProvider) forwardExecution(_ context.Context, responseCtx context.Context, body []byte, _ http.Header, _ string) (_ *http.Response, resultErr error) {
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
				_, _ = io.WriteString(writer, "data: "+string(mustMarshalJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": "instant-held", "status": "in_progress", "output": []any{}}}))+"\n\n")
				select {
				case <-p.gate:
					_, _ = io.WriteString(writer, routerFaultSSE(map[string]any{"type": "response.completed", "response": map[string]any{"id": "instant-held", "status": "completed", "output": []any{}}}))
				case <-responseCtx.Done():
					_ = writer.CloseWithError(responseCtx.Err())
				}
			}()
			return response, nil
		}
		return mchangesNestedCodexResponse(1, map[string]any{"type": "custom_tool_call", "id": "instant-exec-item", "call_id": "instant-exec", "name": "exec", "status": "completed", "input": p.program}), nil
	case 2:
		if !bytes.Contains(body, []byte("Steer now")) {
			return nil, fmt.Errorf("steering input missing from replacement request: %.1000s", body)
		}
		if p.mode == "model" {
			return mchangesNestedCodexResponse(2, instantInterruptAnswer("Steered while model response was gated")), nil
		}
		for _, item := range request.Input {
			if item.Type == "custom_tool_call_output" && item.CallID == "instant-exec" {
				_, _, _, p.cell = stockPatchResultState("exec", []byte(item.Output))
			}
		}
		if p.cell == "" {
			return nil, fmt.Errorf("steering did not yield the original Code Mode cell: %.2000s", body)
		}
		return mchangesNestedCodexResponse(2, map[string]any{"type": "function_call", "id": "instant-wait-item", "call_id": "instant-wait", "name": "wait", "status": "completed", "arguments": string(mustMarshalJSON(map[string]any{"cell_id": p.cell, "yield_time_ms": 10000}))}), nil
	case 3:
		for _, item := range request.Input {
			if item.Type == "function_call_output" && item.CallID == "instant-wait" {
				output := strings.Join(executionOutputTexts([]byte(item.Output)), "\n")
				if !strings.Contains(output, "cell-survived") || !strings.Contains(output, "Script completed") {
					return nil, fmt.Errorf("wait did not complete the original cell: %s", output)
				}
				return mchangesNestedCodexResponse(3, instantInterruptAnswer("Steered cell completed")), nil
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
			provider.program = "// @exec: {\"yield_time_ms\": 60000}\nconst result = await tools.exec_command({cmd: " + string(mustMarshalJSON(shell)) + ", workdir: " + string(mustMarshalJSON(workspace)) + ", yield_time_ms: 30000}); text(result.output);"
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, proxy))
			defer server.Close()
			defer close(provider.gate)
			environment := routerFaultCodexEnvironment(t)
			command := func(ctx context.Context) *exec.Cmd {
				cmd := exec.CommandContext(ctx, codex, "app-server", "--enable", "instant_interrupt", "-c", `model_providers.instant_fixture={name="OpenAI",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="instant_fixture"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "features.code_mode=true", "-c", "features.code_mode_host=true", "-c", "include_collaboration_mode_instructions=false")
				cmd.Env, cmd.Dir = environment, workspace
				return cmd
			}
			terminal := startAppResumeTerminalWithProxy(t, command, "", proxy)
			terminal.await("Ready")
			terminal.send("Start held work\r")
			select {
			case <-provider.requests:
			case <-terminal.ctx.Done():
				t.Fatal("first provider request missing")
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
		})
	}
}
