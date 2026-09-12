package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	tr, err := translateGrokRequest(body)
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

func TestGrokCTPContinuation(t *testing.T) {
	codec := mustCTP2Codec(t)
	repeated := strings.Repeat("alpha beta gamma delta epsilon; ", 24)
	arguments := string(mustMarshalJSON(map[string]string{"text": repeated}))
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			body := mustTestJSON(t, map[string]any{
				"model": grokModel, "stream": stream, "instructions": "Decode CTP/2.",
				"input": []any{
					map[string]any{"type": "message", "role": "user", "content": []any{
						map[string]string{"type": "input_text", "text": repeated},
						map[string]string{"type": "input_text", "text": repeated + "second"},
					}},
					map[string]string{"type": "function_call", "name": "run", "call_id": "call", "arguments": arguments},
					map[string]any{"type": "function_call_output", "call_id": "call", "output": []any{
						map[string]string{"type": "input_text", "text": repeated + "\nrow two\n"},
						map[string]string{"type": "input_text", "text": repeated + "\nrow two\n"},
					}},
				},
			})
			request, err := parseResponsesRequest(body)
			if err != nil {
				t.Fatal(err)
			}
			transform, encoded, err := codec.prepareRequest(&request, body)
			if err != nil || transform == nil {
				t.Fatalf("codec: %v", err)
			}
			if len(transform.sources) != 2 || transform.sources[0].locator != "call/0" || transform.sources[1].locator != "call/1" {
				t.Fatalf("multipart visible-line identities: %+v", transform.sources)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var projected struct {
					Messages []struct {
						Content   json.RawMessage `json:"content"`
						ToolCalls []struct {
							Function struct {
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&projected); err != nil {
					t.Error(err)
					return
				}
				if len(projected.Messages) != 4 {
					t.Errorf("messages: %d", len(projected.Messages))
					return
				}
				if got := projected.Messages[2].ToolCalls[0].Function.Arguments; got != arguments || !json.Valid([]byte(got)) {
					t.Errorf("historical arguments lost JSON identity")
				}
				for _, index := range []int{1, 3} {
					var parts []struct {
						Text string `json:"text"`
					}
					if err := json.Unmarshal(projected.Messages[index].Content, &parts); err != nil || len(parts) != 2 {
						t.Errorf("multipart content flattened: %s", projected.Messages[index].Content)
						continue
					}
					for partIndex, part := range parts {
						expected := repeated
						if index == 1 && partIndex == 1 {
							expected += "second"
						} else if index == 3 {
							expected += "\nrow two\n"
						}
						decoded, err := decodeCTP2String(part.Text, transform.sources, upstreamJSONBufferBytes)
						if err != nil || decoded != expected || part.Text == expected {
							t.Errorf("part %d/%d lost eligible CTP content: %v", index, partIndex, err)
						}
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, grokTextStream())
			}))
			defer server.Close()
			client := &grokClient{httpClient: grokTestHTTPClient(t, server), auth: newGrokAuth("", "xai-test")}
			response, err := client.forwardExecution(t.Context(), t.Context(), encoded, grokTestHeaders())
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			result, err := io.ReadAll(response.Body)
			if err != nil || !bytes.Contains(result, []byte("GROK_OK")) {
				t.Fatalf("continuation: %s, %v", result, err)
			}
		})
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
