package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestReceivedReplyIsFullInChildAndRootCommentary(t *testing.T) {
	for _, messageType := range []string{"MESSAGE", "FINAL_ANSWER"} {
		t.Run(messageType, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
					proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
					root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
					body := strings.Repeat("完整 evidence ", 100) + "FINAL DETAIL"
					envelope := map[string]any{
						"type": "agent_message", "id": "reply", "author": "/root/a", "recipient": "/root/b",
						"content": []any{map[string]any{"type": "input_text", "text": "Message Type: " + messageType + "\nTask name: /root/b\nSender: /root/a\nPayload:\n" + body}},
					}
					child, request := prepareActivityTest(t, proxy, "child", "b", "r", "/root/b", []any{envelope})
					if !bytes.Contains(request.fields["input"], []byte(body)) {
						t.Fatal("original model-visible reply changed")
					}
					response := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{assistantCommentaryMessage("answer", "Substantive answer.")}})
					for _, transform := range []*mekugiResponseTransform{child, root} {
						var output []byte
						if stream {
							events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(response)}))
							if err != nil {
								t.Fatal(err)
							}
							output = bytes.Join(events, nil)
						} else {
							var err error
							output, err = transform.TransformJSON(response)
							if err != nil {
								t.Fatal(err)
							}
						}
						if !bytes.Contains(output, []byte(body)) || bytes.Contains(output, []byte("[excerpt]")) {
							t.Fatal("received reply was shortened")
						}
						if bytes.LastIndex(output, []byte("Substantive answer.")) < bytes.LastIndex(output, []byte(body)) {
							t.Fatal("commentary replaced the substantive answer")
						}
					}
				})
			}
		})
	}
}

func TestReceivedReplyOverBudgetIsOmittedWithoutChangingInput(t *testing.T) {
	body := strings.Repeat("x", maxCommentaryPublicationBytes)
	envelope := func(id, payload string) map[string]any {
		return map[string]any{
			"type": "agent_message", "id": id, "author": "/root/a", "recipient": "/root/b",
			"content": []any{map[string]any{"type": "input_text", "text": "Message Type: MESSAGE\nTask name: /root/b\nSender: /root/a\nPayload:\n" + payload}},
		}
	}
	original := mustMarshalJSON([]any{envelope("oversized", body), envelope("small", "Complete small reply.")})
	fields := map[string]json.RawMessage{"input": original}
	messages := prepareSubagentInputCommentary(fields, "/root/b")
	if !bytes.Equal(fields["input"], original) {
		t.Fatal("oversized reply changed model-visible input")
	}
	if len(messages) != 1 || commentaryText(t, messages[0]) != "[`/root/b` <- `/root/a`] Reply received:\nComplete small reply." {
		t.Fatal("oversized reply was excerpted or consumed the next reply's budget")
	}
}

func TestReceivedReplyResumeProjectsOnlyCurrentInput(t *testing.T) {
	envelope := func(id string) map[string]any {
		return map[string]any{
			"type": "agent_message", "id": id, "author": "/root/worker", "recipient": "/root",
			"content": []any{map[string]any{"type": "input_text", "text": "Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/worker\nPayload:\n" + id}},
		}
	}
	for name, boundary := range map[string]map[string]any{
		"user":        {"type": "message", "role": "user", "content": "Next question"},
		"short_user":  {"role": "user", "content": "Next question"},
		"answer":      {"type": "message", "role": "assistant", "content": "Previous answer"},
		"tool_call":   {"type": "function_call", "id": "call-item", "call_id": "call", "name": "lookup", "arguments": "{}"},
		"custom_call": {"type": "custom_tool_call", "id": "custom-item", "call_id": "custom", "name": "lookup", "input": "{}"},
		"reasoning":   {"type": "reasoning", "id": "reasoning-item", "summary": []any{}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
					// A fresh proxy has neither live deduplication nor previously
					// emitted commentary for the resumed historical envelopes.
					input := []any{envelope("old-reply"), boundary, envelope("fresh-reply")}
					transform, _, request := newSubagentCommentaryTestTransform(t, input)
					if !bytes.Contains(request.fields["input"], []byte("old-reply")) ||
						!bytes.Contains(request.fields["input"], []byte("fresh-reply")) {
						t.Fatal("original envelopes disappeared from model input")
					}
					response := mustTestJSON(t, map[string]any{
						"status": "completed", "output": []any{map[string]any{
							"type": "message", "id": "answer", "role": "assistant", "phase": "final_answer",
							"content": []any{map[string]any{"type": "output_text", "text": "Current answer"}},
						}},
					})
					var output []byte
					if stream {
						events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
							"type": "response.completed", "response": json.RawMessage(response),
						}))
						if err != nil {
							t.Fatal(err)
						}
						output = bytes.Join(events, nil)
					} else {
						var err error
						output, err = transform.TransformJSON(response)
						if err != nil {
							t.Fatal(err)
						}
					}
					if bytes.Contains(output, []byte("old-reply")) ||
						!bytes.Contains(output, []byte("fresh-reply")) ||
						!bytes.Contains(output, []byte("Current answer")) {
						t.Fatalf("resumed reply projection: %s", output)
					}
				})
			}
		})
	}
}

func TestReceivedEncryptedReplyNativeContent(t *testing.T) {
	header := map[string]any{"type": "input_text", "text": "Message Type: MESSAGE\nTask name: /root\nSender: /root/worker\nPayload:\n"}
	ciphertext := map[string]any{"type": "encrypted_content", "encrypted_content": "opaque-test-value"}
	for name, content := range map[string][]any{
		"native":           {header, ciphertext},
		"single_encrypted": {ciphertext},
		"reversed":         {ciphertext, header},
		"extra_part":       {header, ciphertext, header},
		"two_text_parts":   {header, header},
		"empty":            {},
	} {
		t.Run(name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
					envelope := map[string]any{
						"type": "agent_message", "id": "encrypted-reply", "author": "/root/worker",
						"recipient": "/root", "content": content,
					}
					transform, _, request := newSubagentCommentaryTestTransform(t, []any{envelope})
					var input []map[string]json.RawMessage
					if err := json.Unmarshal(request.fields["input"], &input); err != nil {
						t.Fatal(err)
					}
					if len(input) == 0 || jsonString(input[len(input)-1], "id") != "encrypted-reply" ||
						!bytes.Equal(input[len(input)-1]["content"], mustTestJSON(t, content)) {
						t.Fatal("original envelope content changed")
					}
					response := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{}})
					var output []byte
					if stream {
						events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
							"type": "response.completed", "response": json.RawMessage(response),
						}))
						if err != nil {
							t.Fatal(err)
						}
						output = bytes.Join(events, nil)
					} else {
						var err error
						output, err = transform.TransformJSON(response)
						if err != nil {
							t.Fatal(err)
						}
					}
					wantReceipt := name == "native" || name == "single_encrypted"
					notice := []byte("[`/root` <- `/root/worker`] Message received.")
					if bytes.Contains(output, notice) != wantReceipt {
						t.Fatalf("receipt=%v, output=%s", wantReceipt, output)
					}
					if bytes.Contains(output, []byte("opaque-test-value")) || bytes.Contains(output, []byte("Message Type:")) {
						t.Fatal("encrypted envelope content leaked into commentary")
					}
				})
			}
		})
	}
}
