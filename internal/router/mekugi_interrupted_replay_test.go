package router

import (
	"encoding/json"
	"testing"
)

func TestMekugiDeliveredInputSurvivesIncompleteItem(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "code-mode"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			proxy := newManagedMekugiProxy(t)
			dir := t.TempDir()
			store, err := openMekugiReplayStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			proxy.replayStore = store
			var transform *mekugiResponseTransform
			if native {
				transform, _ = newNativeMekugiTestTransformWithProxy(t, proxy)
			} else {
				transform, _, _, _ = newMekugiTestTransformWithProxy(t, proxy)
			}
			added := testMekugiItem()
			added["status"], added["input"] = "in_progress", ""
			if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response.output_item.added", "item": added,
			})); err != nil {
				t.Fatal(err)
			}
			if events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response.custom_tool_call_input.done", "item_id": "item-H", "input": testShellEditSource,
			})); err != nil || len(events) != 2 {
				t.Fatalf("complete input handoff: %s, %v", events, err)
			}
			before, found, err := store.lookup(t.Context(), transform.directory, "call-H")
			if err != nil || !found {
				t.Fatalf("retained handoff: %v, %v", found, err)
			}
			done := testMekugiItem()
			done["status"] = "incomplete"
			if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response.output_item.done", "item": done,
			})); err != nil {
				t.Fatalf("interrupted item after handoff: %v", err)
			}
			if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type":     "response.incomplete",
				"response": map[string]any{"status": "incomplete", "output": []any{done}},
			})); err != nil {
				t.Fatalf("interrupted response after handoff: %v", err)
			}
			reopened, err := openMekugiReplayStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			after, found, err := reopened.lookup(t.Context(), transform.directory, "call-H")
			if err != nil || !found {
				t.Fatalf("restart lookup: %v, %v", found, err)
			}
			if calls != 0 || after.Script != before.Script || after.carrierInput() != before.carrierInput() ||
				jsonString(after.UpstreamItem, "status") != "incomplete" {
				t.Fatalf("interruption changed execution or lost final status: evaluations=%d", calls)
			}
			after.UpstreamItem["input"] = json.RawMessage(`"changed"`)
			if err := reopened.put(t.Context(), transform.directory, map[string]mekugiHistory{"call-H": after}); err == nil {
				t.Fatal("accepted changed input after interruption")
			}
		})
	}
}
