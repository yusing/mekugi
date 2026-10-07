package claude

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/internal/session"
)

func assertDecode(t *testing.T, a *adapter, frame string, want []session.Event) {
	t.Helper()
	got, err := a.decode([]byte(frame))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded events = %#v, want %#v", got, want)
	}
}

func TestAdapterCommandOutputAndBackgroundLaunch(t *testing.T) {
	var a adapter
	assertDecode(t, &a, `{"kind":"command_output","id":"call","caller":"parent","taskID":"shell","text":"tail\n","truncated":true,"done":false}`, []session.Event{{Kind: "command_output", ID: "call", Caller: "parent", Text: "tail\n", Output: &session.CommandOutput{TaskID: "shell", Truncated: true}}})
	assertDecode(t, &a, `{"kind":"command_output","id":"call","taskID":"shell","text":"","done":true,"outputFile":"/native/path with spaces.output"}`, []session.Event{{Kind: "command_output", ID: "call", Output: &session.CommandOutput{TaskID: "shell", OutputFile: "/native/path with spaces.output", Done: true}}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call","content":"Native launch"}]},"tool_use_result":{"backgroundTaskId":"shell"}}}`, []session.Event{{Kind: "tool_result", ID: "call", Text: "Native launch", Output: &session.CommandOutput{TaskID: "shell"}}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"failed","content":"Native failure","is_error":true}]},"tool_use_result":"Native failure"}}`, []session.Event{{Kind: "tool_result", ID: "failed", Text: "Native failure", Failed: true}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"task_notification","task_id":"shell","tool_use_id":"call","status":"completed","output_file":"/native/shell.output"}}`, []session.Event{{Kind: "task", Role: "task_notification", Task: &session.Task{ID: "shell", ToolID: "call", Status: "completed"}}})
	for _, frame := range []string{`{"kind":"command_output","taskID":"shell"}`, `{"kind":"command_output","id":"call"}`, `{"kind":"command_output","id":"call","taskID":"shell","outputFile":"relative","done":true}`, `{"kind":"command_output","id":"call","taskID":"shell","outputFile":"/native/output","done":false}`} {
		if _, err := a.decode([]byte(frame)); err == nil {
			t.Fatal("invalid snapshot admitted")
		}
	}
}

func TestAdapterSessionAndTextStreaming(t *testing.T) {
	var a adapter
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"init","session_id":"session-1","model":"claude-model"}}`, []session.Event{{Kind: "session", SessionID: "session-1", Model: "claude-model"}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"stream_event","event":{"type":"message_start","message":{"id":"message-1"}}}}`, nil)
	assertDecode(t, &a, `{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}}}`, []session.Event{{Kind: "message", ID: "message-1", Role: "Claude", Text: "Hello "}})
	// Subagent text has a separate stream key and must not overwrite the main message.
	assertDecode(t, &a, `{"kind":"event","event":{"type":"stream_event","parent_tool_use_id":"agent-1","event":{"type":"message_start","message":{"id":"message-2"}}}}`, nil)
	assertDecode(t, &a, `{"kind":"event","event":{"type":"stream_event","parent_tool_use_id":"agent-1","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Child"}}}}`, []session.Event{{Kind: "message", ID: "message-2", Role: "Claude", Text: "Child", Caller: "agent-1"}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}}}`, []session.Event{{Kind: "message", ID: "message-1", Role: "Claude", Text: "Hello world"}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"assistant","message":{"id":"message-1","content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]}}}`, []session.Event{{Kind: "message", ID: "message-1", Role: "Claude", Text: "Hello world"}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"result","session_id":"session-1","is_error":false}}`, []session.Event{{Kind: "done", SessionID: "session-1"}})
}

func TestAdapterSideStreamingDoesNotBorrowMain(t *testing.T) {
	var a adapter
	assertDecode(t, &a, `{"kind":"event","event":{"type":"stream_event","event":{"type":"message_start","message":{"id":"same-id"}}}}`, nil)
	assertDecode(t, &a, `{"kind":"side","id":"side-1","frame":{"kind":"event","event":{"type":"stream_event","event":{"type":"message_start","message":{"id":"same-id"}}}}}`, nil)
	assertDecode(t, &a, `{"kind":"side","id":"side-1","frame":{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Side"}}}}}`, []session.Event{{Kind: "message", ID: "same-id", Role: "Claude", Text: "Side", SideID: "side-1"}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Main"}}}}`, []session.Event{{Kind: "message", ID: "same-id", Role: "Claude", Text: "Main"}})
	assertDecode(t, &a, `{"kind":"side","id":"side-1","frame":{"kind":"side_closed"}}`, []session.Event{{Kind: "side_closed", SideID: "side-1"}})
	if len(a.sides) != 0 {
		t.Fatal("closed native side retained stream state")
	}
}

func TestAdapterResumedSessionJournalReset(t *testing.T) {
	var a adapter
	assertDecode(t, &a, `{"kind":"session","sessionID":"old-session"}`, []session.Event{{Kind: "session", SessionID: "old-session"}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"init","session_id":"old-session"}}`, []session.Event{{Kind: "session", SessionID: "old-session"}})
	assertDecode(t, &a, `{"kind":"reset_ready","id":"reset-1"}`, []session.Event{{Kind: "reset_ready", ID: "reset-1"}})
	wrong := `{"kind":"event","event":{"type":"system","subtype":"init","session_id":"new-session"}}`
	if _, err := a.decode([]byte(wrong)); err == nil {
		t.Fatal("preparation authorized a new native identity")
	}
	for _, id := range []string{"new-session", "next-session"} {
		assertDecode(t, &a, fmt.Sprintf(`{"kind":"reset","id":"reset-1","sessionID":%q}`, id), []session.Event{{Kind: "reset", ID: "reset-1", SessionID: id}})
		assertDecode(t, &a, fmt.Sprintf(`{"kind":"event","event":{"type":"system","subtype":"init","session_id":%q}}`, id), []session.Event{{Kind: "session", SessionID: id}})
		if _, err := a.decode([]byte(`{"kind":"event","event":{"type":"system","subtype":"init","session_id":"old-session"}}`)); err == nil {
			t.Fatal("old identity remained authorized after reset")
		}
	}
}

func TestAdapterInvalidResetCannotChangeResumeIdentity(t *testing.T) {
	for _, frame := range []string{
		`{"kind":"reset","id":"reset-1","sessionID":"other","failed":true}`,
		`{"kind":"reset","id":"reset-1"}`,
		`{"kind":"reset","sessionID":"other"}`,
	} {
		a := adapter{resumeSession: "original"}
		if _, err := a.decode([]byte(frame)); err == nil {
			t.Fatal("invalid reset receipt accepted")
		}
		if a.resumeSession != "original" {
			t.Fatal("invalid reset changed expected native identity")
		}
	}
}

func TestAdapterNativeSettingsDiscoveryAndReceipts(t *testing.T) {
	var a adapter
	assertDecode(t, &a, `{"kind":"ready","commandInfo":[{"name":"compact"}],"models":[{"value":"sonnet","resolvedModel":"claude-sonnet","displayName":"Sonnet","description":"Native","supportsEffort":true,"supportedEffortLevels":["low","high"]}]}`, []session.Event{{Kind: "ready", CommandInfo: []session.Command{{Name: "compact"}}, Models: []session.Model{{ID: "sonnet", Resolved: "claude-sonnet", Name: "Sonnet", Description: "Native", SupportsEffort: true, Efforts: []string{"low", "high"}}}}})
	assertDecode(t, &a, `{"kind":"settings","id":"1","field":"effort","value":"high","failed":true,"text":"Native restriction"}`, []session.Event{{Kind: "settings", Settings: &session.Settings{ID: "1", Field: "effort", Value: "high"}, Failed: true, Text: "Native restriction"}})
}

func TestAdapterSessionCommandCatalog(t *testing.T) {
	var a adapter
	assertDecode(t, &a, `{"kind":"commands","commandInfo":[{"name":"workspace-b"}]}`, []session.Event{{Kind: "commands", CommandInfo: []session.Command{{Name: "workspace-b"}}}})
	assertDecode(t, &a, `{"kind":"commands","commandInfo":[]}`, []session.Event{{Kind: "commands", CommandInfo: []session.Command{}}})
}

func TestAdapterToolLifecycle(t *testing.T) {
	var a adapter
	assertDecode(t, &a, `{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"tool-1","name":"Bash","input":{}}}}}`, []session.Event{{Kind: "tool", ID: "tool-1", Role: "Bash", Text: "{}"}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"assistant","message":{"id":"message-1","content":[{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"printf 'hello'","timeout":1000}}]}}}`, []session.Event{{Kind: "tool", ID: "tool-1", Role: "Bash", Text: `{"command":"printf 'hello'","timeout":1000}`}, {Kind: "command_preview", ID: "tool-1", Role: "Bash", CommandInput: &session.CommandInput{Text: "printf 'hello'", Complete: true}}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","content":"hello","is_error":false},{"type":"tool_result","tool_use_id":"tool-2","content":[{"type":"text","text":"failed"},{"type":"image","source":{}},{"type":"text","text":"exit 1"}],"is_error":true}]}}}`, []session.Event{{Kind: "tool_result", ID: "tool-1", Text: "hello"}, {Kind: "tool_result", ID: "tool-2", Text: "failed\nexit 1", Failed: true}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"result","session_id":"session-1","is_error":true,"errors":["permission denied","turn failed"]}}`, []session.Event{{Kind: "done", SessionID: "session-1", Failed: true, Text: "permission denied\nturn failed"}})
}

func TestAdapterPermissionAndQuestions(t *testing.T) {
	var a adapter
	assertDecode(t, &a, `{"kind":"permission","id":"permission-1","tool":"Bash","description":"Execute command","input":{"command":"pwd"}}`, []session.Event{{Kind: "prompt", Prompt: &session.Prompt{ID: "permission-1", Tool: "Bash", Description: "Execute command\n{\"command\":\"pwd\"}"}}})
	assertDecode(t, &a, `{"kind":"permission","id":"question-1","tool":"AskUserQuestion","description":"Choose","input":{"questions":[{"question":"Which backend?","header":"Backend","multiSelect":false,"options":[{"label":"Claude","description":"Native SDK"},{"label":"Codex","description":"App server"}]},{"question":"Which tools?","header":"Tools","multiSelect":true,"options":[{"label":"Read","description":"Read files"}]}]}}`, []session.Event{{Kind: "prompt", Prompt: &session.Prompt{ID: "question-1", Tool: "AskUserQuestion", Description: "Choose", Questions: []session.Question{{Text: "Which backend?", Header: "Backend", Options: []session.Option{{Label: "Claude", Description: "Native SDK"}, {Label: "Codex", Description: "App server"}}}, {Text: "Which tools?", Header: "Tools", Multiple: true, Options: []session.Option{{Label: "Read", Description: "Read files"}}}}}}})
	assertDecode(t, &a, `{"kind":"permission_cancelled","id":"permission-1"}`, []session.Event{{Kind: "dismiss", ID: "permission-1"}})
}

func TestAdapterIgnoredAndMalformedFrames(t *testing.T) {
	for _, frame := range []string{
		`{"kind":"future","payload":true}`,
		`{"kind":"event","event":{"type":"future"}}`,
		`{"kind":"event","event":{"type":"system","subtype":"status"}}`,
		`{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"orphan"}}}}`,
		`{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{}"}}}}`,
	} {
		var a adapter
		assertDecode(t, &a, frame, nil)
	}
	for _, frame := range []string{`{`, `{"kind":123}`, `{"kind":"event","event":{"type":"assistant","message":{"content":"wrong"}}}`, `{"kind":"permission","tool":"AskUserQuestion","input":{"questions":"wrong"}}`} {
		var a adapter
		if _, err := a.decode([]byte(frame)); err == nil {
			t.Errorf("expected rejection of malformed frame %s", frame)
		}
	}
	var a adapter
	assertDecode(t, &a, `{"kind":"ready"}`, []session.Event{{Kind: "ready"}})
	assertDecode(t, &a, `{"kind":"error","text":"native query failed"}`, []session.Event{{Kind: "error", Text: "native query failed"}})
}

func TestAdapterNativePerBlockCompletion(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		t.Run(fmt.Sprint(streamed), func(t *testing.T) {
			var a adapter
			var frames []string
			if streamed {
				frames = append(frames,
					`{"kind":"event","event":{"type":"stream_event","event":{"type":"message_start","message":{"id":"m"}}}}`,
					`{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"First "}}}}`,
					`{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"block."}}}}`)
			}
			frames = append(frames, `{"kind":"event","event":{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"First block."}]}}}`)
			if streamed {
				frames = append(frames, `{"kind":"event","event":{"type":"stream_event","event":{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"Second "}}}}`)
			}
			frames = append(frames, `{"kind":"event","event":{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"Second block."}]}}}`)
			var text string
			for _, frame := range frames {
				events, err := a.decode([]byte(frame))
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range events {
					if e.Kind == "message" {
						text = e.Text
					}
				}
			}
			if text != "First block.Second block." {
				t.Fatalf("same-ID blocks lost or duplicated: %q", text)
			}
		})
	}
}
func TestAdapterStringUserContent(t *testing.T) {
	for _, frame := range []string{
		`{"kind":"event","event":{"type":"user","message":{"role":"user","content":"Native user text"}}}`,
		`{"kind":"event","event":{"type":"user","parent_tool_use_id":"child","message":{"content":"Child user text"}}}`,
		`{"kind":"event","event":{"type":"user","message":{"content":[{"type":"text","text":"array text"}]}}}`,
	} {
		var a adapter
		assertDecode(t, &a, frame, nil)
	}
}

func TestAdapterNativeCommandOutput(t *testing.T) {
	var a adapter
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"local_command_output","content":"Native command output","uuid":"output-1","session_id":"s"}}`, []session.Event{{Kind: "message", ID: "output-1", Role: "Claude", Text: "Native command output"}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"local_command_output","content":"Updated output","uuid":"output-1","session_id":"s"}}`, []session.Event{{Kind: "message", ID: "output-1", Role: "Claude", Text: "Updated output"}})
}
