package codexbinding

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestVisibleResultNames(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"spawn", `{"task_name":"/root/child","thread_id":"host","state":"confirmed"}`, `{"task_name":"agent:main/child","thread_id":"host","state":"confirmed"}`},
		{"roster", `{"agents":[{"agent_name":"/root","wait":{"targets":["host-id"]}}],"effects":[{"task":"/root/child"}]}`, `{"agents":[{"agent_name":"agent:main","wait":{"targets":["host-id"]}}],"effects":[{"task":"agent:main/child"}]}`},
		{"wake", `{"targets":["/root/child"],"metrics":{"/root/child":{"context_tokens":9007199254740993}},"event":{"task":"/root/child","message":"Read /root/file"},"id":"/root/internal"}`, `{"targets":["agent:main/child"],"metrics":{"agent:main/child":{"context_tokens":9007199254740993}},"event":{"task":"agent:main/child","message":"Read /root/file"},"id":"/root/internal"}`},
		{"unrelated", `{"task_name":"/rooted","message":"/root/file","targets":null}`, `{"task_name":"/rooted","message":"/root/file","targets":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(tc.source)
			got, err := visibleResultNames(input)
			if err != nil || !responses.SameJSONValue(got, []byte(tc.want)) || string(input) != tc.source {
				t.Fatalf("projection = %s, %v; input = %s", got, err, input)
			}
			again, err := visibleResultNames(got)
			if err != nil || !bytes.Equal(got, again) {
				t.Fatalf("projection is not idempotent: %s, %v", again, err)
			}
		})
	}
}

func TestProjectRetainedAgentNames(t *testing.T) {
	const source = "Message Type: MESSAGE\nTask name: /root/child\nSender: /root\nPayload:\nKeep /root/file and Task name: /root unchanged."
	const want = "Message Type: MESSAGE\nTask name: agent:main/child\nSender: agent:main\nPayload:\nKeep /root/file and Task name: /root unchanged."
	for _, role := range []string{"assistant", "user"} {
		item := map[string]any{"type": "message", "id": control.MessageItemID("message"), "role": role, "content": []map[string]string{{"type": "output_text", "text": source}}}
		input := responses.MustMarshalJSON([]any{item})
		got, err := ProjectLocalIdentities(input)
		if role == "assistant" {
			delete(item, "id")
			item["content"] = []map[string]string{{"type": "output_text", "text": want}}
		}
		if err != nil || !responses.SameJSONValue(got, responses.MustMarshalJSON([]any{item})) {
			t.Fatalf("%s message projection = %s, %v", role, got, err)
		}
	}
	for _, tc := range []struct{ name, source, want string }{
		{"followup_task", "Operation ID: retained\n" + source, "Operation ID: retained\n" + want},
		{"completion_guard", "Operation ID: retained\nActive descendants: /root/a, /root/a/b. Call wait_agent before completing.", "Operation ID: retained\nActive descendants: agent:main/a, agent:main/a/b. Call wait_agent before completing."},
		{"wait_agent", `{"id":"retained","events":[{"task":"/root/a","status":"failed"},{"task":"/root/b","status":"completed"}]}`, `{"events":[{"status":"failed","task":"agent:main/a"},{"status":"completed","task":"agent:main/b"}]}`},
		{"wait_agent", `{"id":"retained","targets":["/root/child"],"event":{"task":"/root/child","message":"/root/file"}}`, `{"targets":["agent:main/child"],"event":{"task":"agent:main/child","message":"/root/file"}}`},
	} {
		item := map[string]any{"type": "function_call_output", "namespace": "threadcontrol", "name": tc.name, "output": tc.source}
		input := responses.MustMarshalJSON([]any{item})
		got, err := ProjectLocalIdentities(input)
		if err != nil {
			t.Fatal(err)
		}
		var projected []map[string]jsontext.Value
		if err := json.Unmarshal(got, &projected); err != nil {
			t.Fatal(err)
		}
		text := responses.StringField(projected[0], "output")
		if tc.name == "wait_agent" {
			if !responses.SameJSONValue([]byte(text), []byte(tc.want)) {
				t.Fatalf("wake projection = %s", text)
			}
		} else if text != tc.want {
			t.Fatalf("%s projection = %s", tc.name, text)
		}
		again, err := ProjectLocalIdentities(got)
		if err != nil || !bytes.Equal(got, again) {
			t.Fatalf("%s projection is not idempotent", tc.name)
		}
	}
}
