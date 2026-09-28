//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
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

// questionCodexProvider makes the installed Codex emit the real tool call and
// records each subsequent provider request. No live model or account is used.
type questionCodexProvider struct {
	mu       sync.Mutex
	mode     string
	requests chan []byte
	gate     chan struct{}
	turns    int
}

func (p *questionCodexProvider) forwardExecution(ctx, _ context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
	p.mu.Lock()
	p.turns++
	turn := p.turns
	p.mu.Unlock()
	select {
	case p.requests <- bytes.Clone(body):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.mode == "async" && turn == 2 {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if turn > 1 {
		return routerFaultCodexSuccessResponse(), nil
	}
	name, args := "request_user_input", map[string]any{"questions": []any{map[string]any{
		"id": "scope", "header": "Scope", "question": "Which release scope?",
		"options": []any{map[string]any{"label": "Narrow", "description": "Only the affected path"}, map[string]any{"label": "Broad", "description": "Every path"}},
	}}}
	if p.mode == "async" {
		name, args = "request_user_input_async", map[string]any{"questions": []any{map[string]any{
			"title": "Who should receive the update?", "options": []string{"Customers", "Internal team"},
		}}}
	}
	item := map[string]any{"type": "function_call", "id": fmt.Sprintf("question-item-%s", p.mode), "call_id": fmt.Sprintf("question-call-%s", p.mode), "name": name, "arguments": string(mustMarshalJSON(args)), "status": "completed"}
	response := map[string]any{"id": "question-response", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}
	return routerFaultCodexStreamResponse(routerFaultSSE(
		map[string]any{"type": "response.created", "response": map[string]any{"id": "question-response", "status": "in_progress", "output": []any{}}},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item},
		map[string]any{"type": "response.function_call_arguments.done", "item_id": item["id"], "arguments": item["arguments"]},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item},
		map[string]any{"type": "response.completed", "response": response},
	)), nil
}

func TestUserInputQuestionsNativeCodexE2E(t *testing.T) {
	for _, mode := range []string{"async", "sync"} {
		t.Run(mode, func(t *testing.T) {
			codex, err := exec.LookPath("codex")
			if err != nil {
				t.Fatal("installed Codex is required")
			}
			provider := &questionCodexProvider{mode: mode, requests: make(chan []byte, 8), gate: make(chan struct{})}
			gateClosed := false
			server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil, nil))
			defer server.Close()
			defer func() {
				if !gateClosed {
					close(provider.gate)
				}
			}()

			environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
			terminal := startAppResumeTerminal(t, func(ctx context.Context) *exec.Cmd {
				cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.questions={name="questions",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="questions"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false", "-c", "features.default_mode_request_user_input=true")
				cmd.Env, cmd.Dir = environment, workspace
				return cmd
			}, "")
			next := func() []byte {
				t.Helper()
				select {
				case request := <-provider.requests:
					return request
				case <-terminal.ctx.Done():
					t.Fatalf("no provider request: %v\n%s", terminal.ctx.Err(), terminal.screen.String())
					return nil
				}
			}
			terminal.await("Ready")
			terminal.send("Ask the release question\r")
			_ = next()
			if mode == "async" {
				terminal.await("Who should receive the update?")
				_ = next() // The async tool returns immediately; keep the turn running.
				terminal.send("\x02q")
				terminal.await("1 of 1")
				terminal.send("1\r")
				terminal.awaitMatch("accepted question steer", func(screen string) bool {
					return strings.Contains(screen, "Steering after") && strings.Contains(screen, "Working") && !strings.Contains(screen, "Sending…")
				})
				close(provider.gate)
				gateClosed = true
				request := next()
				var parsed struct {
					Input []any `json:"input"`
				}
				if err := json.Unmarshal(request, &parsed); err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(parsed.Input)
				if strings.Count(string(encoded), "<send_user_message_question_reply>") != 1 || !strings.Contains(string(encoded), `request_user_input_async`) || !strings.Contains(string(encoded), `Customers`) {
					t.Fatal("async reply was not included exactly once in the mid-turn provider input")
				}
			} else {
				terminal.await("Which release scope?")
				terminal.send("\x02q") // The just-submitted composer is not idle yet.
				terminal.await("1 of 1")
				terminal.send("1\r")
				request := next()
				var observed struct {
					Input []struct {
						Type   string `json:"type"`
						CallID string `json:"call_id"`
						Output string `json:"output"`
					} `json:"input"`
				}
				if err := json.Unmarshal(request, &observed); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, item := range observed.Input {
					if item.Type != "function_call_output" || item.CallID != "question-call-sync" {
						continue
					}
					var result struct {
						Answers map[string]struct {
							Answers []string `json:"answers"`
						} `json:"answers"`
					}
					if err := json.Unmarshal([]byte(item.Output), &result); err != nil {
						t.Fatal(err)
					}
					answers := result.Answers["scope"].Answers
					found = len(answers) == 1 && answers[0] == "Narrow"
				}
				if !found {
					t.Fatal("sync answer was not delivered as the matching tool result")
				}
				terminal.awaitMatch("sync question resolved", func(screen string) bool {
					return !strings.Contains(screen, "turn waiting") && strings.Contains(screen, "Completed")
				})
			}
			terminal.await("completed")
			terminal.quit()
		})
	}
}
