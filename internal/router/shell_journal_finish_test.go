package router

import (
	"encoding/json"
	"testing"
)

func TestShellJournalFinishRequiresMatchingTerminalHostResult(t *testing.T) {
	call := func(id, name, args string) map[string]json.RawMessage {
		return map[string]json.RawMessage{
			"type": mustMarshalJSON("function_call"), "call_id": mustMarshalJSON(id),
			"name": mustMarshalJSON(name), "arguments": mustMarshalJSON(args),
		}
	}
	shell := call("shell", "shell", "")
	output := func(id, text string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"type": mustMarshalJSON("function_call_output"), "call_id": mustMarshalJSON(id), "output": mustMarshalJSON(text)}
	}
	nativeDone := "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\ndone"
	nativeYield := "Wall time: 0.1 seconds\nProcess running with session ID 7\nOutput:\n"
	codeDone := "Script completed\nWall time 0.1 seconds\nOutput:\n{\"exit_code\":0,\"output\":\"done\"}"
	codeYield := "Script running with cell ID C1\nWall time 0.1 seconds\nOutput:\n"
	sessionYield := "Script completed\nWall time 0.1 seconds\nOutput:\n{\"session_id\":7,\"output\":\"partial\"}"
	user := map[string]json.RawMessage{"role": mustMarshalJSON("user"), "content": mustMarshalJSON("new input")}
	for _, test := range []struct {
		name  string
		code  bool
		turn  string
		input []map[string]json.RawMessage
		want  bool
	}{
		{"native terminal", false, "turn", []map[string]json.RawMessage{shell, output("shell", nativeDone)}, true},
		{"native yielded", false, "turn", []map[string]json.RawMessage{shell, output("shell", nativeYield)}, false},
		{"historical unrelated result", false, "turn", []map[string]json.RawMessage{output("old", nativeDone)}, false},
		{"result missing call", false, "turn", []map[string]json.RawMessage{output("shell", nativeDone)}, false},
		{"nonzero", false, "turn", []map[string]json.RawMessage{shell, output("shell", "Wall time: 0.1 seconds\nProcess exited with code 1\nOutput:\n")}, false},
		{"cancelled", false, "turn", []map[string]json.RawMessage{shell, output("shell", "User cancelled tool")}, false},
		{"new turn", false, "next", []map[string]json.RawMessage{shell, output("shell", nativeDone)}, false},
		{"missing turn", false, "", []map[string]json.RawMessage{shell, output("shell", nativeDone)}, false},
		{"steered", false, "turn", []map[string]json.RawMessage{shell, user, output("shell", nativeDone)}, false},
		{"new user after result", false, "turn", []map[string]json.RawMessage{shell, output("shell", nativeDone), user}, false},
		{"another call pending", false, "turn", []map[string]json.RawMessage{call("other", "lookup", "{}"), shell, output("shell", nativeDone)}, false},
		{"later unrelated call", false, "turn", []map[string]json.RawMessage{shell, output("shell", nativeDone), call("other", "lookup", "{}"), output("other", "{}")}, false},
		{"native resumed", false, "turn", []map[string]json.RawMessage{shell, output("shell", nativeYield), call("poll", "write_stdin", `{"session_id":7,"chars":""}`), output("poll", nativeDone)}, true},
		{"wrong session", false, "turn", []map[string]json.RawMessage{shell, output("shell", nativeYield), call("poll", "write_stdin", `{"session_id":8,"chars":""}`), output("poll", nativeDone)}, false},
		{"code terminal", true, "turn", []map[string]json.RawMessage{shell, output("shell", codeDone)}, true},
		// A prior opaque Code Mode call can await several native sessions. Its
		// internals are not continuation proof, but must not block a later finish.
		{"prior opaque session polling", true, "turn", []map[string]json.RawMessage{
			shell, output("shell", sessionYield),
			call("opaque-poll", "exec", ""),
			output("opaque-poll", "Script completed\nWall time 0.1 seconds\nOutput:\n"),
			user, call("last-shell", "shell", ""), output("last-shell", codeDone),
		}, true},
		{"unrelated yielded session before finish", true, "turn", []map[string]json.RawMessage{
			shell, output("shell", sessionYield),
			call("opaque-poll", "exec", ""),
			output("opaque-poll", "Script completed\nWall time 0.1 seconds\nOutput:\n"),
			call("last-shell", "shell", ""), output("last-shell", codeDone),
		}, true},
		{"code yielded", true, "turn", []map[string]json.RawMessage{shell, output("shell", codeYield)}, false},
		{"code terminated", true, "turn", []map[string]json.RawMessage{shell, output("shell", "Script terminated\nWall time 0.1 seconds\nOutput:\n{\"exit_code\":0}")}, false},
		{"code resumed", true, "turn", []map[string]json.RawMessage{shell, output("shell", codeYield), call("wait", "wait", `{"cell_id":"C1"}`), output("wait", codeDone)}, true},
		{"code then session resumed", true, "turn", []map[string]json.RawMessage{shell, output("shell", codeYield), call("wait", "wait", `{"cell_id":"C1"}`), output("wait", sessionYield), call("poll", "write_stdin", `{"session_id":7,"chars":""}`), output("poll", nativeDone)}, true},
		{"code session yielded", true, "turn", []map[string]json.RawMessage{shell, output("shell", sessionYield)}, false},
		{"truncated result", true, "turn", []map[string]json.RawMessage{shell, output("shell", "Script completed\nWall time 0.1 seconds\nOutput:\n{\"exit_code\":")}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newJournalStore()
			if err := store.initialize(t.Context(), nil, "workspace", "thread", "/root", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := store.apply(t.Context(), nil, "workspace", "thread", "runtime:"+shellJournalFinishReceipt("turn", "shell"), nil); err != nil {
				t.Fatal(err)
			}
			if _, err := store.apply(t.Context(), nil, "workspace", "thread", "runtime:"+shellJournalFinishReceipt("turn", "last-shell"), nil); err != nil {
				t.Fatal(err)
			}
			kind := codeModeCarrierFunction
			if test.code {
				kind = codeModeCarrierCustom
			}
			transform := &mekugiResponseTransform{
				ctx: t.Context(), proxy: &mekugiProxy{journals: store, commentary: newCommentaryBroker()},
				directory: "workspace", shellThreadID: "thread", shellTurnID: test.turn, codeModeToolName: "exec",
				visible: map[string]mekugiHistory{
					"shell":      {ToolName: "shell", PluginID: builtinToolsPluginID, ShellJournalTurnID: "turn", CarrierKind: kind},
					"last-shell": {ToolName: "shell", PluginID: builtinToolsPluginID, ShellJournalTurnID: "turn", CarrierKind: kind},
				},
			}
			got, err := transform.shellJournalFinished(mustMarshalJSON(test.input))
			if err != nil || got != test.want {
				t.Fatalf("finished = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestShellJournalFinishReceiptSurvivesRestartButNotFork(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	store := newJournalStore()
	if err := store.initialize(t.Context(), replay, workspace, "thread", "/root", ""); err != nil {
		t.Fatal(err)
	}
	receipt := "runtime:" + shellJournalFinishReceipt("turn", "shell")
	if _, err := store.apply(t.Context(), replay, workspace, "thread", receipt, []journalMutation{{Op: "add", Text: new("done")}}); err != nil {
		t.Fatal(err)
	}
	call := map[string]json.RawMessage{
		"type": mustMarshalJSON("function_call"), "call_id": mustMarshalJSON("shell"), "name": mustMarshalJSON("shell"),
	}
	history := mekugiHistory{ToolName: "shell", PluginID: builtinToolsPluginID, ShellJournalTurnID: "turn", CarrierKind: codeModeCarrierFunction, CarrierName: "exec_command", UpstreamItem: call}
	if err := replay.put(t.Context(), workspace, map[string]mekugiHistory{"shell": history}); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(replay.directory)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := reopened.read(workspace, "shell", false)
	if err != nil || !found {
		t.Fatalf("reopen call: found=%v, %v", found, err)
	}
	restarted := newJournalStore()
	transform := &mekugiResponseTransform{
		ctx: t.Context(), proxy: &mekugiProxy{journals: restarted, replayStore: reopened, commentary: newCommentaryBroker()},
		directory: workspace, shellThreadID: "thread", shellTurnID: "turn", visible: map[string]mekugiHistory{"shell": record.History},
	}
	input := mustMarshalJSON([]any{call, map[string]any{
		"type": "function_call_output", "call_id": "shell", "output": "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n",
	}})
	if got, err := transform.shellJournalFinished(input); err != nil || !got {
		t.Fatalf("restart finish = %v, %v", got, err)
	}
	if err := restarted.initialize(t.Context(), reopened, workspace, "fork", "/root", "thread"); err != nil {
		t.Fatal(err)
	}
	transform.shellThreadID = "fork"
	if got, err := transform.shellJournalFinished(input); err != nil || got {
		t.Fatalf("fork inherited finish = %v, %v", got, err)
	}
}

func TestShellJournalSuccessfulBatchRequiresEveryProgram(t *testing.T) {
	for _, raw := range []string{
		`{"results":[{"exit_code":0},{"exit_code":1}],"batch":{"program_count":2,"not_started_programs":0}}`,
		`{"results":[{"exit_code":0}],"batch":{"program_count":2,"not_started_programs":1}}`,
		`{"results":[{"exit_code":0}],"batch":{"program_count":1,"stopped_reason":"host_error"}}`,
		`{"exit_code":0,"session_id":7}`,
	} {
		if shellJournalSuccessfulJSON([]byte(raw)) {
			t.Fatalf("accepted incomplete or failed result: %s", raw)
		}
	}
	if !shellJournalSuccessfulJSON([]byte(`{"results":[{"exit_code":0},{"exit_code":0}],"batch":{"program_count":2,"not_started_programs":0,"stopped_reason":null}}`)) {
		t.Fatal("rejected completed batch")
	}
}
