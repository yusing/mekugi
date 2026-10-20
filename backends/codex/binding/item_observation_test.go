package binding

import (
	json "encoding/json/v2"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestHistoryItemObservationPreservesPublicFacts(t *testing.T) {
	thread, err := DecodeHistoryThread([]byte(`{"thread":{"id":"child","agentNickname":"Reader",
		"source":{"subAgent":{"thread_spawn":{"agent_path":"/root/reader","agent_role":"worker"}}},
		"turns":[{"id":"turn","items":[
			{"id":"command","type":"commandExecution","source":"userShell","command":"cat file","cwd":"/workspace","status":"completed",
			 "commandActions":[{"type":"read","command":"cat file","path":"file"}],"durationMs":0,"exitCode":0,"aggregatedOutput":""},
			{"id":"web","type":"webSearch","query":"fallback","action":{"type":"search","queries":["one","two"]},"results":[]},
			{"id":"edit","type":"fileChange","changes":[{"path":"old","diff":"patch","kind":{"type":"update","movePath":"new"}},
			 {"path":"old2","kind":{"type":"update","move_path":"snake","movePath":"camel"}}]},
			{"id":"reason","type":"reasoning","text":"Private text","content":["Private reasoning"],"summary":["Public summary"]},
			{"id":"user","type":"userMessage","content":[{"type":"text","text":"User input"}]},
			{"id":"answer","type":"agentMessage","text":"Answer","phase":"final_answer"}
		]}]}}`), "child")
	if err != nil {
		t.Fatal(err)
	}
	if got := thread.Observation(); got != (observation.Thread{ID: "child", Nickname: "Reader", Path: "/root/reader", Role: "worker"}) {
		t.Fatalf("thread metadata = %#v", got)
	}
	items := thread.Turns[0].Items
	command := items[0].Observation()
	if !command.UserShell || command.Command != "cat file" || command.Cwd != "/workspace" || command.Status != "completed" ||
		!reflect.DeepEqual(command.CommandActions, []observation.CommandAction{{Type: "read", Command: "cat file", Path: "file"}}) ||
		command.DurationMS == nil || *command.DurationMS != 0 || command.ExitCode == nil || *command.ExitCode != 0 || command.AggregatedOutput == nil || *command.AggregatedOutput != "" {
		t.Fatalf("command facts = %#v", command)
	}
	web := items[1].Observation()
	if web.Query != "fallback" || web.Results == nil || *web.Results != 0 || !reflect.DeepEqual(web.Action, &observation.WebSearchAction{Type: "search", Queries: []string{"one", "two"}}) {
		t.Fatalf("search facts = %#v", web)
	}
	changes := items[2].Observation().Changes
	if len(changes) != 2 || changes[0].Kind.MovePath != "new" || changes[1].Kind.MovePath != "snake" || changes[0].Diff != "patch" {
		t.Fatalf("change facts = %#v", changes)
	}
	reasoning := items[3].Observation()
	if reasoning.Text != "" || len(reasoning.Content) != 0 || !reflect.DeepEqual(reasoning.Summary, []string{"Public summary"}) {
		t.Fatalf("reasoning facts = %#v", reasoning)
	}
	user := items[4].Observation()
	if string(user.Content) != `[{"type":"text","text":"User input"}]` || items[5].Observation().Text != "Answer" {
		t.Fatal("message content was lost")
	}
	reasoning.Summary[0], web.Action.Queries[0], user.Content[0] = "changed", "changed", 'x'
	if items[3].Summary[0] != "Public summary" || items[3].Text != "Private text" || string(items[3].Content) != `["Private reasoning"]` || items[1].Action.Queries[0] != "one" || items[4].Content[0] != '[' {
		t.Fatal("projection changed the native payload")
	}
}

func TestItemObservationUnknownOutcomesRemainUnknown(t *testing.T) {
	for _, raw := range []string{`{}`, `{"durationMs":null,"exitCode":null,"aggregatedOutput":null,"results":null}`} {
		var item ThreadItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			t.Fatal(err)
		}
		got := item.Observation()
		if got.DurationMS != nil || got.ExitCode != nil || got.AggregatedOutput != nil || got.Results != nil {
			t.Fatalf("unknown outcomes became known: %#v", got)
		}
	}
}
