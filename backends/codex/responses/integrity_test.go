package responses

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestGrokEmptyContentWire(t *testing.T) {
	body := mustTestJSON(t, map[string]any{
		"model": grokModel,
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{}},
			map[string]any{"type": "function_call_output", "call_id": "call", "output": []any{}},
		},
	})
	tr, err := testTranslate(body)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(responses.MustMarshalJSON(tr.body), &wire); err != nil {
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
			tr := &Stream{policy: testPolicy(), endpoint: chatEndpoint{}, tools: map[string]providerTool{"run": {name: "run", kind: "function"}}}
			_, err := tr.Read(strings.NewReader(grokTestSSE(chunks...)), func(event map[string]any) error {
				if event["type"] == "response.function_call_arguments.done" {
					t.Error("exposed executable call after invalid terminal")
				}
				return nil
			}, testFailureDetail)
			if err == nil {
				t.Fatal("accepted post-terminal choice")
			}
		})
	}
	tr := &Stream{policy: testPolicy(), endpoint: chatEndpoint{}}
	result, err := tr.Read(strings.NewReader(grokTextStream()), func(map[string]any) error { return nil }, testFailureDetail)
	if err != nil || result["usage"] == nil || result["status"] != "completed" {
		t.Fatalf("usage trailer rejected: %v, %v", result, err)
	}
}

func TestGrokInterleavedArgumentFragments(t *testing.T) {
	tr := &Stream{policy: testPolicy(), endpoint: chatEndpoint{}, tools: map[string]providerTool{"run": {name: "run", kind: "function"}}}
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
	result, err := tr.Read(strings.NewReader(grokTestSSE(chunks...)), func(map[string]any) error { return nil }, testFailureDetail)
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
			tr := &Stream{policy: testPolicy(), endpoint: chatEndpoint{}, tools: map[string]providerTool{"run": {name: "run", kind: "function"}}}
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				result, err := tr.Read(strings.NewReader(wire), func(map[string]any) error { return nil }, testFailureDetail)
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

func BenchmarkResponsesFragmentedText(b *testing.B) {
	for _, count := range []int{1024, 4096, 16384} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			const delta = "abcdefghijklmnop"
			text := strings.Repeat(delta, count)
			wire := strings.Repeat("data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\""+delta+"\"}\n\n", count)
			wire += grokTestSSE(map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{
				map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": text}}},
			}}})
			tr, err := testTranslateFormat([]byte(`{"input":[]}`), "responses")
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			for b.Loop() {
				result, err := tr.codec.Read(strings.NewReader(wire), func(map[string]any) error { return nil }, testFailureDetail)
				if err != nil {
					b.Fatal(err)
				}
				content := result["output"].([]any)[0].(map[string]any)["content"].([]any)
				if content[0].(map[string]any)["text"] != text {
					b.Fatal("fragmented text changed")
				}
			}
		})
	}
}

func TestResponsesFragmentedTextIntegrity(t *testing.T) {
	for _, kind := range []string{"output_text", "refusal"} {
		for _, final := range []string{"answer", "different"} {
			t.Run(kind+"/"+final, func(t *testing.T) {
				field := "text"
				if kind == "refusal" {
					field = "refusal"
				}
				wire := grokTestSSE(
					map[string]any{"type": "response." + kind + ".delta", "delta": ""},
					map[string]any{"type": "response." + kind + ".delta", "delta": "ans"},
					map[string]any{"type": "response." + kind + ".delta", "delta": "wer"},
					map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{
						map[string]any{"type": "message", "content": []any{map[string]string{"type": kind, field: final}}},
					}}},
				)
				tr, err := testTranslateFormat([]byte(`{"input":[]}`), "responses")
				if err != nil {
					t.Fatal(err)
				}
				result, err := tr.codec.Read(strings.NewReader(wire), func(map[string]any) error { return nil }, testFailureDetail)
				if final != "answer" {
					if err == nil || !strings.Contains(err.Error(), "final text differs") {
						t.Fatalf("mismatched final text: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				part := result["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
				if part["type"] != kind || part[field] != final {
					t.Fatalf("content changed: %v", part)
				}
			})
		}
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
			tr := &Stream{policy: testPolicy(), endpoint: chatEndpoint{}}
			result, err := tr.Read(strings.NewReader(wire), func(event map[string]any) error {
				events = append(events, event)
				return nil
			}, testFailureDetail)
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
