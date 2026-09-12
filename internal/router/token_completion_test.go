package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTokenCommentaryRequiresCompletedSubstantiveAnswer(t *testing.T) {
	message := func(phase, text string) map[string]any {
		item := map[string]any{"type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text}}}
		if phase != "" {
			item["phase"] = phase
		}
		return item

	}
	for _, tc := range []struct {
		name, status, event string
		output              []any
		want                bool
	}{
		{"final", "completed", "", []any{message("final_answer", "Answer")}, true},
		{"legacy", "completed", "", []any{message("", "Answer")}, true},
		{"commentary", "completed", "", []any{message("commentary", "Still working")}, false},
		{"empty", "completed", "", []any{message("final_answer", " ")}, false},
		{"tool", "completed", "", []any{map[string]any{"type": "function_call"}}, false},
		{"mixed", "completed", "", []any{message("final_answer", "Answer"), map[string]any{"type": "custom_tool_call"}}, false},
		{"hosted_tool", "completed", "", []any{message("", "Answer"), map[string]any{"type": "web_search_call", "status": "completed"}}, true},
		{"unfinished_hosted_tool", "completed", "", []any{message("", "Answer"), map[string]any{"type": "web_search_call", "status": "searching"}}, false},
		{"client_search", "completed", "", []any{message("", "Answer"), map[string]any{"type": "tool_search_call", "execution": "client", "status": "completed"}}, false},
		{"server_search", "completed", "", []any{message("", "Answer"), map[string]any{"type": "tool_search_call", "execution": "server", "status": "completed"}}, true},
		{"local_shell", "completed", "", []any{message("", "Answer"), map[string]any{"type": "shell_call", "environment": map[string]any{"type": "local"}, "status": "completed"}}, false},
		{"hosted_shell", "completed", "", []any{message("", "Answer"), map[string]any{"type": "shell_call", "environment": map[string]any{"type": "container_reference"}, "status": "completed"}}, true},

		{"failed", "failed", "", []any{message("final_answer", "Answer")}, false},
		{"incomplete", "incomplete", "", []any{message("final_answer", "Answer")}, false},
		{"missing_body_status", "", "completed", []any{message("final_answer", "Answer")}, true},
		{"event_overrides_body", "failed", "completed", []any{message("final_answer", "Answer")}, true},
		{"failed_event_overrides_body", "completed", "failed", []any{message("final_answer", "Answer")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := mustTestJSON(t, map[string]any{"id": "response", "status": tc.status, "output": tc.output})
			object, notice, err := responseWithTokenUsageCommentary(payload, tokenUsageReport{tokenCounts: tokenCounts{
				InputTokens: 20, UncachedInputTokens: 8, OutputTokens: 5, ReasoningTokens: 3,
			}}, true, tc.event)
			if err != nil {
				t.Fatal(err)
			}
			if (notice != nil) != tc.want {
				t.Fatalf("notice = %s", mustTestJSON(t, notice))
			}
			var output []json.RawMessage
			if err := json.Unmarshal(object["output"], &output); err != nil {
				t.Fatal(err)
			}
			offset := 0
			if tc.want {
				offset = 1
				if !strings.HasPrefix(commentaryText(t, notice), testTokenUsageTable) {
					t.Fatalf("notice = %s", mustTestJSON(t, notice))
				}
			}
			if len(output) != len(tc.output)+offset {
				t.Fatal("substantive output displaced")
			}
			for i, original := range tc.output {
				if string(output[i+offset]) != string(mustTestJSON(t, original)) {
					t.Fatal("provider output changed")
				}
			}
		})
	}
}

func TestTokenCommentaryRequiresCurrentUsageDespitePriorTotals(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			prior, _ := prepareActivityTest(t, proxy, "before", "thread", "", "/root", nil)
			prior.observeResponseUsage(tokenCounts{InputTokens: 20, UncachedInputTokens: 8, OutputTokens: 5, ReasoningTokens: 3})
			prior.Close()
			final, _ := prepareActivityTest(t, proxy, "after", "thread", "", "/root", nil)
			response := map[string]any{
				"id": "answer", "status": "completed",
				"output": []any{map[string]any{"type": "message", "role": "assistant", "phase": "final_answer",
					"content": []any{map[string]any{"type": "output_text", "text": "Actual answer"}}}},
			}
			var output []byte
			if stream {
				events, err := final.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": response}))
				if err != nil {
					t.Fatal(err)
				}
				output = bytes.Join(events, nil)
			} else {
				var err error
				output, err = final.TransformJSON(mustTestJSON(t, response))
				if err != nil {
					t.Fatal(err)
				}
			}
			if final.usageObserved {
				t.Fatal("test unexpectedly observed current usage")
			}
			if strings.Contains(string(output), "Tokens:") || !strings.Contains(string(output), "Actual answer") {
				t.Fatalf("stale token usage was reported or provider answer was lost: %s", output)
			}
			if _, available := final.threadUsageCounts(); !available {
				t.Fatal("prior aggregate was lost")
			}
		})
	}
}
