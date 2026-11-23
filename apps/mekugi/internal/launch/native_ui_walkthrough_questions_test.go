package launch

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestNativeUIWalkthroughQuestionContract(t *testing.T) {
	provider := new(walkthroughProvider)
	for _, scenario := range []struct {
		name, input, tool string
		count             int
	}{
		{"async", `{"input":[{"type":"message","role":"user","content":[{"text":"questions"}]}]}`, "request_user_input_async", 2},
		{"sync", `{"input":[{"type":"function_call_output","call_id":"preview-1-step-async-question"}]}`, "request_user_input", 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			provider.serve(response, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(scenario.input)))
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			found := false
			for _, line := range strings.Split(response.Body.String(), "\n") {
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				var event struct {
					Type string                                 `json:"type"`
					Item struct{ Type, Name, Arguments string } `json:"item"`
				}
				if err := json.Unmarshal([]byte(data), &event, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				if event.Type != "response.output_item.done" {
					continue
				}
				if event.Item.Type != "function_call" || event.Item.Name != scenario.tool {
					t.Fatalf("unexpected question tool: %+v", event.Item)
				}
				var arguments struct {
					Questions []struct {
						Question string                   `json:"question"`
						Header   string                   `json:"header"`
						Options  []binding.QuestionOption `json:"options"`
					} `json:"questions"`
				}
				if err := json.Unmarshal([]byte(event.Item.Arguments), &arguments, json.RejectUnknownMembers(true)); err != nil {
					t.Fatal(err)
				}
				if len(arguments.Questions) != scenario.count {
					t.Fatalf("question count: %d", len(arguments.Questions))
				}
				for _, question := range arguments.Questions {
					if question.Question == "" || len(question.Options) < 2 {
						t.Fatal("missing question/options", question)
					}
					recommended := 0
					for index, option := range question.Options {
						if option.Label == "" || option.Preview == "" {
							t.Fatal("missing label/preview", option)
						}
						if option.Recommended {
							recommended++
							if index == 0 {
								t.Fatal("recommendation should demonstrate non-first focus")
							}
						}
					}
					if recommended != 1 {
						t.Fatalf("recommendations: %d", recommended)
					}
				}
				found = true
			}
			if !found {
				t.Fatal("no question call emitted")
			}
		})
	}
}

func TestNativeUIWalkthroughAnswerSteps(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"sync", `{"input":[{"type":"function_call_output","call_id":"preview-2-step-sync-question","output":"[{\"id\":\"q3\",\"choice\":2}]"}]}`, "sync-question"},
		{"missing sync", `{"input":[{"type":"function_call_output","call_id":"preview-2-step-sync-question"}]}`, "invalid-sync-answer"},
		{"invalid sync", `{"input":[{"type":"function_call_output","call_id":"preview-2-step-sync-question","output":"not JSON"}]}`, "invalid-sync-answer"},
		{"empty sync", `{"input":[{"type":"function_call_output","call_id":"preview-2-step-sync-question","output":"[]"}]}`, "invalid-sync-answer"},
		{"invalid choice", `{"input":[{"type":"function_call_output","call_id":"preview-2-step-sync-question","output":"[{\"id\":\"q3\",\"choice\":-1}]"}]}`, "invalid-sync-answer"},
		{"async after sync", `{"input":[{"type":"function_call_output","call_id":"preview-2-step-sync-question","output":"[{\"id\":\"q3\",\"choice\":2}]"},{"type":"message","role":"user","content":[{"text":"<question_answers>[{\"id\":\"q2\",\"choice\":2}]</question_answers>"}]}]}`, "async-answer"},
		{"invalid async after sync", `{"input":[{"type":"function_call_output","call_id":"preview-2-step-sync-question","output":"[{\"id\":\"q3\",\"choice\":2}]"},{"type":"message","role":"user","content":[{"text":"<question_answers>[]</question_answers>"}]}]}`, "invalid-async-answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request struct {
				Input []walkthroughInput `json:"input"`
			}
			if err := json.Unmarshal([]byte(tc.input), &request); err != nil {
				t.Fatal(err)
			}
			if got := walkthroughStep(request.Input); got != tc.want {
				t.Fatalf("step = %q, want %q", got, tc.want)
			}
			response := httptest.NewRecorder()
			new(walkthroughProvider).serve(response, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tc.input)))
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			want := "The question answer was missing or invalid."
			switch tc.want {
			case "sync-question":
				want = "Release scope received."
			case "async-answer":
				want = "Release update answers received."
			}
			var messages []string
			for _, line := range strings.Split(response.Body.String(), "\n") {
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				var event struct {
					Type string `json:"type"`
					Item struct {
						Type    string `json:"type"`
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"item"`
				}
				if err := json.Unmarshal([]byte(data), &event); err != nil {
					t.Fatal(err)
				}
				if event.Type != "response.output_item.done" {
					continue
				}
				if event.Item.Type != "message" || len(event.Item.Content) != 1 {
					t.Fatalf("unexpected answer output: %+v", event.Item)
				}
				messages = append(messages, event.Item.Content[0].Text)
			}
			if len(messages) != 1 || messages[0] != want {
				t.Fatalf("provider messages = %q, want exactly [%q]", messages, want)
			}
		})
	}
}

func TestNativeUIWalkthroughCapturedEditOutput(t *testing.T) {
	for _, original := range []string{`"Patch applied"`, `[{"type":"input_text","text":"Patch applied"}]`} {
		output, _, err := codexresponses.AppendOutputNotice([]byte(original), "Captured saved edit: internal/pane/launch.go")
		if err != nil {
			t.Fatal(err)
		}
		request := `{"input":[{"type":"custom_tool_call_output","call_id":"preview-1-step-edited","output":` + string(output) + `}]}`
		response := httptest.NewRecorder()
		new(walkthroughProvider).serve(response, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(request)))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "collaboration__spawn_agent") {
			t.Fatalf("captured edit did not launch review: status=%d body=%s", response.Code, response.Body.String())
		}
	}
}
