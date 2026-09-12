package router

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCollaborationCallsPassThroughWithoutCommentary(t *testing.T) {
	for _, namespace := range []string{"collaboration", "native_agents"} {
		for _, name := range []string{"spawn_agent", "followup_task", "send_message", "wait_agent", "interrupt_agent"} {
			t.Run(namespace+"/"+name, func(t *testing.T) {
				transform, proxy, _ := newSubagentCommentaryTestTransform(t, nil)
				call := map[string]any{"type": "function_call", "id": "item", "call_id": "call",
					"namespace": namespace, "name": name, "arguments": `{"message":"opaque-secret","agent_type":"worker"}`}
				original := mustTestJSON(t, call)
				for _, event := range []map[string]any{
					{"type": "response.output_item.added", "item": call},
					{"type": "response.function_call_arguments.delta", "item_id": "item", "delta": "opaque"},
					{"type": "response.function_call_arguments.done", "item_id": "item", "arguments": call["arguments"]},
					{"type": "response.output_item.done", "item": call},
				} {
					payload := mustTestJSON(t, event)
					events, err := transform.TransformSSE(payload)
					if err != nil || len(events) != 1 || !bytes.Equal(events[0], payload) {
						t.Fatalf("collaboration event changed: %s, %v", events, err)
					}
				}
				if err := transform.Finish(true); err != nil {
					t.Fatal(err)
				}
				payload := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}})
				output, err := transform.TransformJSON(payload)
				if err != nil {
					t.Fatal(err)
				}
				var response struct{ Output []json.RawMessage }
				if err := json.Unmarshal(output, &response); err != nil || len(response.Output) != 1 || !bytes.Equal(response.Output[0], original) {
					t.Fatalf("collaboration JSON changed: %s, %v", output, err)
				}
				request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustTestJSON(t, []any{call})}}
				if err := proxy.reconcileInputPrefix(request, transform.historySessionID); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(request.fields["input"], mustTestJSON(t, []any{call})) {
					t.Fatal("collaboration replay changed")
				}
				fields := map[string]json.RawMessage{"input": mustTestJSON(t, []any{
					map[string]any{"type": "additional_tools", "tools": []any{
						map[string]any{"type": "namespace", "name": namespace, "tools": []any{
							map[string]any{"type": "function", "name": name, "parameters": map[string]any{"type": "object"}},
						}},
					}},
				})}
				catalog, err := prepareCommentaryTools(fields, decodeResponsesToolCatalog(fields))
				if err != nil || len(catalog) != 0 {
					t.Fatalf("collaboration alias instrumented: %v, %v", catalog, err)
				}
			})
		}
	}
}

func TestChildProviderCommentaryAdmissionAndDistinctSources(t *testing.T) {
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
	child, _ := prepareActivityTest(t, proxy, "child", "c", "r", "/root/worker", nil)
	orphan, _ := prepareActivityTest(t, proxy, "orphan", "o", "unknown", "/root/orphan", nil)
	for _, mutate := range []func(map[string]json.RawMessage){
		func(item map[string]json.RawMessage) { delete(item, "id") },
		func(item map[string]json.RawMessage) { item["status"] = mustTestJSON(t, "in_progress") },
		func(item map[string]json.RawMessage) { item["phase"] = mustTestJSON(t, "final_answer") },
		func(item map[string]json.RawMessage) { item["role"] = mustTestJSON(t, "user") },
		func(item map[string]json.RawMessage) { item["id"] = mustTestJSON(t, commentaryMessageID("owned")) },
	} {
		item := assistantCommentaryMessage("excluded", "Not ready progress")
		mutate(item)
		child.collectProviderCommentary(item)
	}
	root.collectProviderCommentary(assistantCommentaryMessage("root-progress", "Root progress"))
	orphan.collectProviderCommentary(assistantCommentaryMessage("orphan-progress", "Unknown ancestry"))
	for _, id := range []string{"first", "second"} {
		child.collectProviderCommentary(assistantCommentaryMessage(id, "Same authored text"))
	}
	events, err := root.TransformSSE([]byte(`{"type":"response.in_progress"}`))
	if err != nil || len(events) != 4 {
		t.Fatalf("root activity boundary: %s, %v", events, err)
	}
	var ids []string
	for _, event := range events[1:3] {
		var envelope struct{ Item map[string]json.RawMessage }
		if err := json.Unmarshal(event, &envelope); err != nil {
			t.Fatal(err)
		}
		if commentaryText(t, envelope.Item) != "[`/root/worker`] Same authored text" {
			t.Fatalf("unexpected projection: %s", event)
		}
		ids = append(ids, jsonString(envelope.Item, "id"))
	}
	if ids[0] == ids[1] {
		t.Fatal("distinct source messages merged by text")
	}
}
func TestChildProviderCommentaryForwardsWithoutChangingHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
			root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
			other, _ := prepareActivityTest(t, proxy, "other", "other", "", "/root", nil)
			_, _ = prepareActivityTest(t, proxy, "parent", "p", "r", "/root/worker", nil)
			child, _ := prepareActivityTest(t, proxy, "child", "c", "p", "/root/worker/nested", nil)
			progress := assistantCommentaryMessage("provider-progress", "Checked the caller.\nThe result is consistent.")
			answer := map[string]any{"type": "message", "id": "answer", "role": "assistant", "phase": "final_answer", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "Substantive child answer"}}}
			payload := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{progress, answer}})
			if stream {
				for i, item := range []any{progress, answer} {
					event := mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": item})
					events, err := child.TransformSSE(event)
					// Progress remains live; the answer waits for the terminal.
					if err != nil || len(events) != 1-i || i == 0 && !bytes.Equal(events[0], event) {
						t.Fatalf("child event changed: %s, %v", events, err)
					}

				}
				events, err := child.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(payload)}))
				if err != nil || len(events) != 2 {
					t.Fatalf("child terminal changed: %s, %v", events, err)
				}
				if !bytes.Contains(events[0], []byte("Substantive child answer")) || bytes.Contains(bytes.Join(events, nil), []byte("Journal saved:")) {
					t.Fatalf("child provider result changed: %s", events)
				}
			} else {
				output, err := child.TransformJSON(payload)
				if err != nil || bytes.Contains(output, []byte("Journal saved:")) || !bytes.Contains(output, []byte("Substantive child answer")) {
					t.Fatalf("child JSON changed: %s, %v", output, err)
				}
			}
			visible, err := root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			var response struct{ Output []map[string]json.RawMessage }
			if err := json.Unmarshal(visible, &response); err != nil || len(response.Output) != 3 {
				t.Fatalf("root projection: %s, %v", visible, err)
			}
			// The parent and nested child each announce their first request.
			response.Output = response.Output[2:]
			if commentaryText(t, response.Output[0]) != "[`/root/worker/nested`] Checked the caller.\nThe result is consistent." {
				t.Fatalf("attribution: %s", visible)
			}
			if output, err := other.TransformJSON([]byte(`{"output":[]}`)); err != nil || bytes.Contains(output, []byte("Checked the caller")) {
				t.Fatalf("unrelated root received progress: %s, %v", output, err)
			}
			child.collectProviderCommentary(progress)
			if messages := proxy.activity.drain("r", root.activityStarted, maxCommentaryPublicationBytes); len(messages) != 0 {
				t.Fatal("completed/terminal/replay source duplicated")
			}
			_, replay := prepareActivityTest(t, proxy, "child-next", "c", "p", "/root/worker/nested",
				[]any{response.Output[0], progress, answer})
			if bytes.Contains(replay.fields["input"], response.Output[0]["id"]) ||
				!bytes.Contains(replay.fields["input"], mustTestJSON(t, progress)) ||
				!bytes.Contains(replay.fields["input"], mustTestJSON(t, answer)) {
				t.Fatalf("original child history or root replay changed: %s", replay.fields["input"])
			}
		})
	}
}
