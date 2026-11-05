package executionobservation

import (
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestToolInputEmissionClearsOnItemDoneOrResponseClose(t *testing.T) {
	for _, closeResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "item done", true: "response close"}[closeResponse], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			transform := &mekugiResponseTransform{owners: &Preparer{Config: Config{Control: proxy.Control, Review: proxy.Review, Windows: proxy.Windows, Calls: proxy.Calls, Retained: proxy.Retained, Changes: proxy.Changes, Checkpoints: proxy.Checkpoints, Recovery: proxy.Recovery, PublishChanges: proxy.PublishChanges, PreviewTiming: proxy.PreviewTiming}}, threadID: "main", directory: "/workspace"}
			defer transform.Close()
			item := map[string]any{"id": "emitting", "type": "custom_tool_call", "status": "incomplete"}
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": item})
			if !proxy.Control.EmittingToolInput("/workspace", "main") {
				t.Fatal("emission was not observed")
			}
			if closeResponse {
				transform.Close()
			} else {
				emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.done", "item": item})
			}
			if proxy.Control.EmittingToolInput("/workspace", "main") {
				t.Fatal("ended response still blocks answers")
			}
		})
	}
}

func emissionTestEvent(t *testing.T, transform *mekugiResponseTransform, event map[string]any) {
	t.Helper()
	if _, err := transform.transformActivitySSE(responses.MustMarshalJSON(event)); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantMessageEmissionSettlement(t *testing.T) {
	for _, ending := range []string{"response.completed", "response.failed", "response.incomplete", "close"} {
		t.Run(ending, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			transform := &mekugiResponseTransform{owners: &Preparer{Config: Config{Control: proxy.Control, Review: proxy.Review}}, threadID: "child", directory: "/workspace"}
			defer transform.Close()
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "message", "type": "message"}})
			if proxy.Control.AssistantMessage("/workspace", "child") == 0 || proxy.Control.AssistantMessage("/workspace", "main") != 0 || proxy.Control.AssistantMessage("/other", "child") != 0 || proxy.Control.EmittingToolInput("/workspace", "child") {
				t.Fatal("message gate escaped its authenticated scope or kind")
			}
			if ending == "close" {
				transform.Close()
			} else {
				emissionTestEvent(t, transform, map[string]any{"type": ending, "response": map[string]any{"id": "response", "output": []any{}}})
			}
			if proxy.Control.AssistantMessage("/workspace", "child") != 0 {
				t.Fatal("settled generation still blocks input")
			}
		})
	}
}
