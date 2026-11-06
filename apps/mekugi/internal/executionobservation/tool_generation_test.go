package executionobservation

import (
	"bytes"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
)

func TestToolGenerationStreamsBeforePayloadCompletion(t *testing.T) {
	for _, test := range []struct{ kind, name, event, delta, want string }{
		{"function_call", "request_user_input_async", "response.function_call_arguments.delta", `{"questions":[`, "Asking user question"},
		{"function_call", "exec_command", "response.function_call_arguments.delta", `{"cmd":"long`, "Executing a command"},
		{"custom_tool_call", "apply_patch", "response.custom_tool_call_input.delta", "*** Begin Patch\n", "Emitting a patch"},
		{"custom_tool_call", "exec", "response.custom_tool_call_input.delta", `await tools.mcp__threadcontrol.spawn_agent({task_name:"worker",message:"long`, "Spawning subagent `worker`"},
		{"function_call", "exec", "response.function_call_arguments.delta", `await tools.exec_command({cmd:"long`, "Executing a command"},
		{"function_call", "mcp__other__lookup", "response.function_call_arguments.delta", `{"query":"long`, "Calling `mcp__other__lookup`"},
	} {
		t.Run(test.kind+"/"+test.name, func(t *testing.T) {
			o := new(sessioncontrol.Observations)
			transform := &mekugiResponseTransform{owners: &Preparer{Config: Config{Control: o}}, generationOnly: true, threadID: "thread", codeModeToolName: "exec"}
			forward := func(event map[string]any) {
				t.Helper()
				payload := mustTestJSON(t, event)
				visible, err := transform.TransformSSE(payload)
				if err != nil || len(visible) != 1 || !bytes.Equal(visible[0], payload) {
					t.Fatalf("changed stock stream: %q, %v", visible, err)
				}
				transform.Delivered(visible[0])
			}
			forward(map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": test.kind, "id": "item", "call_id": "call", "name": test.name}})
			forward(map[string]any{"type": test.event, "item_id": "item", "delta": test.delta})
			if got := o.ToolInput("thread"); got != test.want {
				t.Fatalf("input still incomplete: %q, want %q", got, test.want)
			}
			forward(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": test.kind, "id": "item", "call_id": "call", "name": test.name, "status": "incomplete"}})
			if o.ToolInput("thread") != "" {
				t.Fatal("item completion did not retire generation")
			}
			forward(map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": test.kind, "id": "next", "name": test.name}})
			transform.Close()
			if o.ToolInput("thread") != "" {
				t.Fatal("transport close did not retire generation")
			}
		})
	}
}

func TestToolGenerationSettlesAfterItemDelivery(t *testing.T) {
	for _, event := range []string{"response.function_call_arguments.done", "response.custom_tool_call_input.done", "response.output_item.done", "response.completed", "response.failed", "response.incomplete"} {
		t.Run(event, func(t *testing.T) {
			o := new(sessioncontrol.Observations)
			transform := &mekugiResponseTransform{owners: &Preparer{Config: Config{Control: o}}, generationOnly: true, threadID: "thread"}
			o.SetToolInput(&transform.generation, "", "thread", sessioncontrol.GenerationItem{ID: "item"}, true)
			o.IdentifyToolInput(&transform.generation, sessioncontrol.GenerationItem{ID: "item"}, "apply_patch", false, "")
			payload := mustTestJSON(t, map[string]any{"type": event, "item_id": "item", "item": map[string]any{"type": "custom_tool_call", "id": "item"}})
			if _, err := transform.TransformSSE(payload); err != nil {
				t.Fatal(err)
			}
			if o.ToolInput("thread") != "Emitting a patch" || !o.EmittingToolInput("", "thread") {
				t.Fatal("transformation retired input before delivery")
			}
			transform.Delivered(payload)
			argumentsDone := event == "response.function_call_arguments.done" || event == "response.custom_tool_call_input.done"
			if argumentsDone != o.EmittingToolInput("", "thread") || argumentsDone != (o.ToolInput("thread") != "") {
				t.Fatal("generation did not follow delivered item/exchange completion")
			}
		})
	}
}
