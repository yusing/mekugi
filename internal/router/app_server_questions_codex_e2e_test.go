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
	for _, tc := range []struct {
		mode       string
		image      bool
		bareArrows bool
	}{{"async", false, false}, {"sync", false, false}, {"async", true, false}, {"sync", true, false}, {"async", false, true}, {"sync", false, true}} {
		name := tc.mode
		if tc.image {
			name += "-image"
		}
		if tc.bareArrows {
			name += "-arrows"
		}
		t.Run(name, func(t *testing.T) {
			mode := tc.mode
			imagePath := ""
			if tc.image {
				imagePath = answerImageFixture(t)
			}
			answer := func(terminal *appResumeTerminal) {
				t.Helper()
				if tc.bareArrows {
					terminal.send("one two\x1b[D\x1b[DX\x1b[CY")
					terminal.await("one tXwYo")
					terminal.send("\r")
					return
				}
				if !tc.image {
					terminal.send("1\r")
					return
				}
				terminal.send("\x1b[200~" + imagePath + "\x1b[201~")
				terminal.await("[Image 1]")
				terminal.send("\r")
			}
			codex, err := exec.LookPath("codex")
			if err != nil {
				t.Fatal("installed Codex is required")
			}
			provider := &questionCodexProvider{mode: mode, requests: make(chan []byte, 8), gate: make(chan struct{})}
			gateClosed := false
			server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
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
				answer(terminal)
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
				wantAnswer := map[bool]string{false: "Customers", true: "[Image 1]"}[tc.image]
				if tc.bareArrows {
					wantAnswer = "one tXwYo"
				}
				if strings.Count(string(encoded), "<send_user_message_question_reply>") != 1 || !strings.Contains(string(encoded), `request_user_input_async`) || !strings.Contains(string(encoded), wantAnswer) {
					t.Fatal("async reply was not included exactly once in the mid-turn provider input")
				}
				if tc.image {
					assertQuestionProviderImage(t, request, imagePath)
				}
			} else {
				terminal.await("Which release scope?")
				terminal.send("\x02q") // The just-submitted composer is not idle yet.
				terminal.await("1 of 1")
				answer(terminal)
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
					want := "Narrow"
					if tc.bareArrows {
						want = "user_note: one tXwYo"
					}
					if tc.image {
						want = "user_note: [Image 1] "
					}
					found = len(answers) == 1 && answers[0] == want
				}
				if !found {
					t.Fatal("sync answer was not delivered as the matching tool result")
				}
				if tc.image {
					if !bytes.Contains(request, []byte(`"type":"input_image"`)) {
						request = next()
					}
					assertQuestionProviderImage(t, request, imagePath)
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

func assertQuestionProviderImage(t *testing.T, request []byte, path string) {
	t.Helper()
	var body struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				URL  string `json:"image_url"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(request, &body); err != nil {
		t.Fatal(err)
	}
	images, framed := 0, false
	for _, item := range body.Input {
		if item.Role != "user" {
			continue
		}
		for i, part := range item.Content {
			if part.Type != "input_image" {
				continue
			}
			images++
			if !strings.HasPrefix(part.URL, "data:image/png;base64,") {
				t.Fatal("answer image bytes missing")
			}
			if i > 0 && i+1 < len(item.Content) {
				framed = strings.HasPrefix(item.Content[i-1].Text, "<image") && strings.Contains(item.Content[i-1].Text, path) && item.Content[i+1].Text == "</image>"
			}
		}
	}
	if images != 1 || !framed {
		t.Fatalf("answer did not reuse the host image frame: images=%d framed=%v", images, framed)
	}
}
