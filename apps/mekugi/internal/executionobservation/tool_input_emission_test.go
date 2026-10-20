package executionobservation

import (
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestToolInputEmissionClearsOnItemDoneOrResponseClose(t *testing.T) {
	for _, closeResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "item done", true: "response close"}[closeResponse], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			transform := &mekugiResponseTransform{control: proxy.control, review: proxy.review, execWindows: proxy.execWindows, calls: proxy.calls, retained: proxy.retained, changeSource: proxy.changeSource, snapshots: proxy.snapshots, recovered: proxy.recovered, publishChanges: proxy.publishChanges, previewTiming: proxy.previewTiming, threadID: "main", directory: "/workspace"}
			defer transform.Close()
			item := map[string]any{"id": "emitting", "type": "custom_tool_call", "status": "incomplete"}
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": item})
			if !proxy.control.EmittingToolInput("/workspace", "main") {
				t.Fatal("emission was not observed")
			}
			if closeResponse {
				transform.Close()
			} else {
				emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.done", "item": item})
			}
			if proxy.control.EmittingToolInput("/workspace", "main") {
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
