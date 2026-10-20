package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestReceivedRepliesRemainNativeAndKeepFullBodies(t *testing.T) {
	body := strings.Repeat("完整🙂", 300) + " FINAL DETAIL"
	envelope := func(id, kind, text string) map[string]any {
		return map[string]any{
			"type": "agent_message", "id": id, "author": "/root/a", "recipient": "/root/b",
			"content": []any{map[string]any{"type": "input_text", "text": "Message Type: " + kind + "\nTask name: /root/b\nSender: /root/a\nPayload:\n" + text}},
		}
	}
	input := mustTestJSON(t, []any{envelope("long", "MESSAGE", body), envelope("short", "MESSAGE", "Next reply."), envelope("done", "FINAL_ANSWER", body)})
	fields := map[string]json.RawMessage{"input": input}
	got := prepareSubagentInputEnvelopes(fields, "/root/b")
	if !bytes.Equal(fields["input"], input) {
		t.Fatal("native envelopes changed")
	}
	if len(got.replies) != 2 || got.replies[0].message != (activityMessage{from: "/root/a", to: "/root/b", text: body}) ||
		got.replies[0].source == "" || got.replies[1].message.text != "Next reply." ||
		len(got.finals) != 1 || got.finals[0].text != body {
		t.Fatalf("native replies and finals = %+v", got)
	}
}

func TestReceivedReplyCurrentInputBoundary(t *testing.T) {
	envelope := func(id string) map[string]any {
		return map[string]any{"type": "agent_message", "id": id, "author": "/root/worker", "recipient": "/root",
			"content": []any{map[string]any{"type": "input_text", "text": "Message Type: MESSAGE\nTask name: /root\nSender: /root/worker\nPayload:\n" + id}}}
	}
	for name, boundary := range map[string]map[string]any{
		"user":      {"type": "message", "role": "user", "content": "Next question"},
		"assistant": {"type": "message", "role": "assistant", "content": "Previous answer"},
		"tool":      {"type": "function_call", "id": "call", "name": "lookup", "arguments": "{}"},
		"reasoning": {"type": "reasoning", "id": "thought", "summary": []any{}},
	} {
		t.Run(name, func(t *testing.T) {
			input := mustTestJSON(t, []any{envelope("old"), boundary, envelope("fresh")})
			fields := map[string]json.RawMessage{"input": input}
			got := prepareSubagentInputEnvelopes(fields, "/root")
			if len(got.replies) != 1 || got.replies[0].message != (activityMessage{from: "/root/worker", to: "/root", text: "fresh"}) || !bytes.Equal(fields["input"], input) {
				t.Fatalf("current native reply = %+v; input = %s", got.replies, fields["input"])
			}
		})
	}
}

func TestReceivedEncryptedReplyDoesNotExposeCiphertext(t *testing.T) {
	header := map[string]any{"type": "input_text", "text": "Message Type: MESSAGE\nTask name: /root\nSender: /root/worker\nPayload:\n"}
	ciphertext := map[string]any{"type": "encrypted_content", "encrypted_content": "opaque-test-value"}
	for name, content := range map[string][]any{
		"native": {header, ciphertext}, "single encrypted": {ciphertext},
		"reversed": {ciphertext, header}, "extra part": {header, ciphertext, header},
	} {
		t.Run(name, func(t *testing.T) {
			input := mustTestJSON(t, []any{map[string]any{"type": "agent_message", "id": name, "author": "/root/worker", "recipient": "/root", "content": content}})
			fields := map[string]json.RawMessage{"input": input}
			got := prepareSubagentInputEnvelopes(fields, "/root")
			want := name == "native" || name == "single encrypted"
			if (len(got.replies) == 1) != want || want && got.replies[0].message != (activityMessage{from: "/root/worker", to: "/root"}) || !bytes.Equal(fields["input"], input) {
				t.Fatalf("encrypted reply = %+v; input = %s", got.replies, fields["input"])
			}
		})
	}
}
