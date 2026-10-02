package claude

import "testing"

func TestHistoryRestoresMixedBlocksWithoutLiveEffects(t *testing.T) {
	var a adapter
	events, err := a.decode([]byte(`{"kind":"history","event":{"type":"assistant","uuid":"message","message":{"content":[{"type":"text","text":"Proposal"},{"type":"tool_use","id":"call","name":"Write","input":{"file_path":"never-create","content":"x"}}]}}}`))
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if events[0].Text != "Proposal" || events[1].Kind != "tool" || events[1].ID != "history/call" {
		t.Fatalf("mixed blocks lost: %+v", events)
	}
	for _, e := range events {
		if !e.Historical || e.Edit != nil || e.Prompt != nil {
			t.Fatalf("replayed effect: %+v", e)
		}
	}
	if len(a.tools) != 0 || len(a.streams) != 0 {
		t.Fatal("history revived streaming state")
	}
	events, err = a.decode([]byte(`{"kind":"history","event":{"type":"user","uuid":"result","message":{"content":[{"type":"tool_result","tool_use_id":"call","content":"denied","is_error":true}]}}}`))
	if err != nil || len(events) != 1 || events[0].ID != "history/call" || !events[0].Historical || !events[0].Failed {
		t.Fatalf("result=%+v err=%v", events, err)
	}
}

func TestHistoryUserAndNativeCommands(t *testing.T) {
	var a adapter
	events, err := a.decode([]byte(`{"kind":"history","event":{"type":"user","uuid":"user","message":{"content":"Original intent"}}}`))
	if err != nil || len(events) != 1 || events[0].Role != "You" || events[0].Text != "Original intent" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	events, err = a.decode([]byte(`{"kind":"ready","commandInfo":[{"name":"compact"},{"name":"btw"}]}`))
	if err != nil || len(events) != 1 || len(events[0].CommandInfo) != 2 {
		t.Fatalf("commands=%+v err=%v", events, err)
	}
}
