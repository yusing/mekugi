package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestGrokEmptyContentWire(t *testing.T) {
	body := mustTestJSON(t, map[string]any{
		"model": grokModel,
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{}},
			map[string]any{"type": "function_call_output", "call_id": "call", "output": []any{}},
		},
	})
	tr, err := translateChatRequest(body, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(mustMarshalJSON(tr.body), &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 2 {
		t.Fatalf("messages = %d", len(wire.Messages))
	}
	for index, role := range []string{"user", "tool"} {
		message := wire.Messages[index]
		if message.Role != role || string(message.Content) != `""` {
			t.Fatalf("message %d: role=%q content=%s", index, message.Role, message.Content)
		}
	}
}

func TestGrokStreamSealsTerminalChoice(t *testing.T) {
	choice := func(delta any, finish string) any {
		return map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	tool := map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call", "function": map[string]string{"name": "run", "arguments": "{}"}}}}
	for name, chunks := range map[string][]any{
		"conflicting reason": {choice(nil, "length"), choice(tool, "tool_calls")},
		"post-terminal text": {choice(nil, "stop"), choice(map[string]string{"content": "late"}, "")},
		"post-terminal call": {choice(nil, "tool_calls"), choice(tool, "")},
		"duplicate terminal": {choice(nil, "stop"), choice(nil, "stop")},
	} {
		t.Run(name, func(t *testing.T) {
			tr := &grokTranslation{tools: map[string]grokTool{"run": {name: "run", kind: "function"}}}
			_, err := tr.readGrokStream(strings.NewReader(grokTestSSE(chunks...)), func(event map[string]any) error {
				if event["type"] == "response.function_call_arguments.done" {
					t.Error("exposed executable call after invalid terminal")
				}
				return nil
			})
			if err == nil {
				t.Fatal("accepted post-terminal choice")
			}
		})
	}
	tr := &grokTranslation{}
	result, err := tr.readGrokStream(strings.NewReader(grokTextStream()), func(map[string]any) error { return nil })
	if err != nil || result["usage"] == nil || result["status"] != "completed" {
		t.Fatalf("usage trailer rejected: %v, %v", result, err)
	}
}

func TestGrokInterleavedArgumentFragments(t *testing.T) {
	tr := &grokTranslation{tools: map[string]grokTool{"run": {name: "run", kind: "function"}}}
	arguments := []string{`{"first":"αβγ"}`, `{"second":[1,2,3]}`}
	var chunks []any
	for offset := range max(len([]rune(arguments[0])), len([]rune(arguments[1]))) {
		for index, value := range arguments {
			runes := []rune(value)
			if offset >= len(runes) {
				continue
			}
			fn := map[string]string{"arguments": string(runes[offset])}
			part := map[string]any{"index": index, "function": fn}
			if offset == 0 {
				part["id"] = fmt.Sprint("call", index)
				fn["name"] = "run"
			}
			chunks = append(chunks, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{part}}}}})
		}
	}
	chunks = append(chunks, map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls"}}})
	result, err := tr.readGrokStream(strings.NewReader(grokTestSSE(chunks...)), func(map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for index, output := range result["output"].([]any) {
		if got := output.(map[string]any)["arguments"]; got != arguments[index] {
			t.Fatalf("call %d: %q != %q", index, got, arguments[index])
		}
	}
}

func BenchmarkGrokFragmentedArguments(b *testing.B) {
	for _, size := range []int{1024, 4096, 16384} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			var chunks []any
			for index, fragment := range []string{`{"value":"`, strings.Repeat("x", size), `"}`} {
				for offset := 0; offset < len(fragment); offset += 16 {
					call := map[string]any{"index": 0, "function": map[string]string{"arguments": fragment[offset:min(offset+16, len(fragment))]}}
					if index == 0 && offset == 0 {
						call["id"] = "call"
						call["function"].(map[string]string)["name"] = "run"
					}
					chunks = append(chunks, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{call}}}}})
				}
			}
			chunks = append(chunks, map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls"}}})
			wire := grokTestSSE(chunks...)
			tr := &grokTranslation{tools: map[string]grokTool{"run": {name: "run", kind: "function"}}}
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				result, err := tr.readGrokStream(strings.NewReader(wire), func(map[string]any) error { return nil })
				if err != nil {
					b.Fatal(err)
				}
				got := result["output"].([]any)[0].(map[string]any)["arguments"].(string)
				if len(got) != size+12 {
					b.Fatalf("argument size: %d", len(got))
				}
			}
		})
	}
}

func TestGrokFinishEvidence(t *testing.T) {
	for _, test := range []struct{ reason, status, detail string }{
		{"stop", "completed", ""},
		{"length", "incomplete", "max_output_tokens"},
		{"content_filter", "incomplete", "content_filter"},
		{"", "", ""}, {"future", "", ""}, {"tool_calls", "", ""},
	} {
		t.Run(test.reason, func(t *testing.T) {
			wire := grokTestSSE(map[string]any{"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"content": "answer"}, "finish_reason": test.reason,
			}}})
			var events []map[string]any
			tr := &grokTranslation{}
			result, err := tr.readGrokStream(strings.NewReader(wire), func(event map[string]any) error {
				events = append(events, event)
				return nil
			})
			if test.status == "" {
				if err == nil {
					t.Fatalf("accepted %q", test.reason)
				}
				for _, event := range events {
					if event["type"] == "response.completed" || event["type"] == "response.incomplete" {
						t.Fatal("invalid choice emitted terminal")
					}
				}
				return
			}
			if err != nil || result["status"] != test.status || events[len(events)-1]["type"] != "response."+test.status {
				t.Fatalf("result=%v err=%v", result, err)
			}
			if test.detail != "" {
				details, ok := result["incomplete_details"].(map[string]string)
				if !ok || details["reason"] != test.detail {
					t.Fatal(result)
				}
			}
		})
	}
}
