package router

import (
	"bytes"
	jsonv1 "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

// Source: internal/router/shell_journal_finish_test.go@518ac19ac89d17d4ef8b038422b46b124811dc17
// TestShellJournalFinishRequiresMatchingTerminalHostResult and restart coverage.
// The current contract observes stock host execution, not a shell plugin result.
func TestJournalHostFinishRequiresMatchingTerminalHostResult(t *testing.T) {
	call := func(id, name, args string) map[string]jsonv1.RawMessage {
		return map[string]jsonv1.RawMessage{"type": mustMarshalJSON("function_call"), "call_id": mustMarshalJSON(id), "name": mustMarshalJSON(name), "arguments": mustMarshalJSON(args)}
	}
	output := func(id, value string) map[string]jsonv1.RawMessage {
		return map[string]jsonv1.RawMessage{"type": mustMarshalJSON("function_call_output"), "call_id": mustMarshalJSON(id), "output": mustMarshalJSON(value)}
	}
	host := call("host", "exec_command", `{"cmd":"printf done"}`)
	done := "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\ndone"
	yielded := "Wall time: 0.1 seconds\nProcess running with session ID 7\nOutput:\n"
	user := map[string]jsonv1.RawMessage{"role": mustMarshalJSON("user"), "content": mustMarshalJSON("new input")}
	for _, test := range []struct {
		name, turn, executingThread string
		input                       []map[string]jsonv1.RawMessage
		want                        bool
	}{
		{"terminal", "turn", "thread", []map[string]jsonv1.RawMessage{host, output("host", done)}, true},
		{"yielded", "turn", "thread", []map[string]jsonv1.RawMessage{host, output("host", yielded)}, false},
		{"historical unrelated result", "turn", "thread", []map[string]jsonv1.RawMessage{output("old", done)}, false},
		{"result missing call", "turn", "thread", []map[string]jsonv1.RawMessage{output("host", done)}, false},
		{"nonzero", "turn", "thread", []map[string]jsonv1.RawMessage{host, output("host", "Process exited with code 1\nOutput:\n")}, false},
		{"cancelled", "turn", "thread", []map[string]jsonv1.RawMessage{host, output("host", "User cancelled tool")}, false},
		{"new turn", "next", "thread", []map[string]jsonv1.RawMessage{host, output("host", done)}, false},
		{"missing turn", "", "thread", []map[string]jsonv1.RawMessage{host, output("host", done)}, false},
		{"wrong executing thread", "turn", "other", []map[string]jsonv1.RawMessage{host, output("host", done)}, false},
		{"steered", "turn", "thread", []map[string]jsonv1.RawMessage{host, user, output("host", done)}, false},
		{"new user after result", "turn", "thread", []map[string]jsonv1.RawMessage{host, output("host", done), user}, false},
		{"another call pending", "turn", "thread", []map[string]jsonv1.RawMessage{call("other", "lookup", "{}"), host, output("host", done)}, false},
		{"later unrelated call", "turn", "thread", []map[string]jsonv1.RawMessage{host, output("host", done), call("other", "lookup", "{}"), output("other", "{}")}, false},
		{"resumed", "turn", "thread", []map[string]jsonv1.RawMessage{host, output("host", yielded), call("poll", "write_stdin", `{"session_id":7,"chars":""}`), output("poll", done)}, true},
		{"wrong session", "turn", "thread", []map[string]jsonv1.RawMessage{host, output("host", yielded), call("poll", "write_stdin", `{"session_id":8,"chars":""}`), output("poll", done)}, false},
		{"resumed nonzero", "turn", "thread", []map[string]jsonv1.RawMessage{host, output("host", yielded), call("poll", "write_stdin", `{"session_id":7,"chars":""}`), output("poll", "Process exited with code 9\nOutput:\n")}, false},
		{"poll still yielded", "turn", "thread", []map[string]jsonv1.RawMessage{host, output("host", yielded), call("poll", "write_stdin", `{"session_id":7,"chars":""}`), output("poll", yielded)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newJournalStore()
			if err := store.initialize(t.Context(), nil, "workspace", "thread", "/root", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := store.apply(t.Context(), nil, "workspace", "thread", "runtime:"+journalHostFinishReceipt("turn", "host"), nil); err != nil {
				t.Fatal(err)
			}
			transform := &mekugiResponseTransform{ctx: t.Context(), proxy: &mekugiProxy{journals: store, commentary: newCommentaryBroker()}, directory: "workspace", shellThreadID: "thread", shellTurnID: test.turn,
				visible: map[string]mekugiHistory{"host": {ToolName: "exec_command", ExecutingThread: test.executingThread, JournalFinishTurnID: "turn", CarrierKind: codeModeCarrierFunction, CarrierName: "exec_command", UpstreamItem: host}}}
			capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
			recorder, err := capturer.New(capturer.Config{Output: capturePath, Mode: "mekugi"})
			if err != nil {
				t.Fatal(err)
			}
			defer recorder.Close()
			handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Fatal(err)
				}
				transform.ctx = r.Context()
				attempt := &requestAttempt{startCtx: r.Context(), mekugiTransform: transform, request: parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustMarshalJSON(test.input)}, streamResponse: true}}
				if got, err := attempt.tryJournalHostFinish(); err != nil || got != test.want {
					t.Fatalf("finished = %v, %v; want %v", got, err, test.want)
				}
				if attempt.response != nil {
					defer attempt.response.Body.Close()
					w.Header().Set("Content-Type", attempt.response.Header.Get("Content-Type"))
					if _, err := io.Copy(w, attempt.response.Body); err != nil {
						t.Fatal(err)
					}
				}
			}))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(mustMarshalJSON(map[string]any{"model": "model", "input": test.input}))))
			raw, err := os.ReadFile(capturePath)
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				ProviderExpected *bool `json:"provider_expected"`
			}
			if err := jsonv1.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			if test.want != (record.ProviderExpected != nil && !*record.ProviderExpected) {
				t.Fatalf("provider expectation = %v; local finish = %v", record.ProviderExpected, test.want)
			}
			snapshot := recorder.Snapshot()
			if test.want && (snapshot.Requests.Completed != 1 || snapshot.Requests.ProviderAttempts != 0 || snapshot.Capture.MissingProvider != 0 || snapshot.Capture.CaptureErrors != 0) {
				t.Fatalf("local finish export does not reconcile: %+v", snapshot)
			}
		})
	}
}

func TestJournalHostFinishReceiptSurvivesRestartButNotFork(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	store := newJournalStore()
	if err := store.initialize(t.Context(), replay, workspace, "thread", "/root", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.apply(t.Context(), replay, workspace, "thread", "runtime:"+journalHostFinishReceipt("turn", "host"), []journalMutation{{Op: "add", Title: new("done")}}); err != nil {
		t.Fatal(err)
	}
	call := map[string]jsonv1.RawMessage{"type": mustMarshalJSON("function_call"), "call_id": mustMarshalJSON("host"), "name": mustMarshalJSON("exec_command")}
	history := mekugiHistory{ToolName: "exec_command", ExecutingThread: "thread", JournalFinishTurnID: "turn", CarrierKind: codeModeCarrierFunction, CarrierName: "exec_command", UpstreamItem: call}
	if err := replay.put(t.Context(), workspace, map[string]mekugiHistory{"host": history}); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(replay.directory)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := reopened.read(workspace, "host", false)
	if err != nil || !found {
		t.Fatalf("reopen call: found=%v, %v", found, err)
	}
	restarted := newJournalStore()
	transform := &mekugiResponseTransform{ctx: t.Context(), proxy: &mekugiProxy{journals: restarted, replayStore: reopened, commentary: newCommentaryBroker()}, directory: workspace, shellThreadID: "thread", shellTurnID: "turn", visible: map[string]mekugiHistory{"host": record.History}}
	input := mustMarshalJSON([]any{call, map[string]any{"type": "function_call_output", "call_id": "host", "output": "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n"}})
	if got, err := transform.journalHostFinished(input); err != nil || !got {
		t.Fatalf("restart finish = %v, %v", got, err)
	}
	if err := restarted.initialize(t.Context(), reopened, workspace, "fork", "/root", "thread"); err != nil {
		t.Fatal(err)
	}
	transform.shellThreadID = "fork"
	if got, err := transform.journalHostFinished(input); err != nil || got {
		t.Fatalf("fork inherited finish = %v, %v", got, err)
	}
}

func TestJournalHostFinishCodeModeRequiresNativeTrace(t *testing.T) {
	for _, tool := range []string{"exec_command", "write_stdin"} {
		arguments := `{"cmd":"printf done"}`
		if tool == "write_stdin" {
			arguments = `{"session_id":7,"chars":""}`
		}
		source := `await tools.` + tool + `(` + arguments + `); await journal({op:"finish"});`
		for _, test := range []struct {
			name, status string
			result       any
			end, trace   bool
			want         bool
			followup     any
			pollSession  int
		}{
			{"real success without printed result", "completed", map[string]any{"exit_code": 0}, true, true, true, nil, 0},
			{"printed success without trace", "completed", map[string]any{"exit_code": 0}, true, false, false, nil, 0},
			{"cell unfinished", "completed", map[string]any{"exit_code": 0}, false, true, false, nil, 0},
			{"caught failed tool", "failed", nil, true, true, false, nil, 0},
			{"caught nonzero exit", "completed", map[string]any{"exit_code": 7}, true, true, false, nil, 0},
			{"nested tool yielded", "completed", map[string]any{"session_id": 7}, true, true, false, nil, 0},
			{"successful repeated polling", "completed", map[string]any{"session_id": 7}, true, true, true, map[string]any{"exit_code": 0}, 7},
			{"failed repeated polling", "completed", map[string]any{"session_id": 7}, true, true, false, map[string]any{"exit_code": 7}, 7},
			{"still yielding repeated polling", "completed", map[string]any{"session_id": 7}, true, true, false, map[string]any{"session_id": 7}, 7},
			{"different session completed", "completed", map[string]any{"session_id": 7}, true, true, false, map[string]any{"exit_code": 0}, 8},
		} {
			t.Run(tool+"/"+test.name, func(t *testing.T) {
				fixture := newNativeTraceFixture(t)
				fixture.start("thread", "cell", "host", source)
				fixture.tool("thread", "cell", "nested", tool, arguments)
				fixture.result("thread", "nested", test.status, test.result)
				if test.followup != nil {
					fixture.tool("thread", "cell", "poll", "write_stdin", fmt.Sprintf(`{"session_id":%d,"chars":""}`, test.pollSession))
					fixture.result("thread", "poll", "completed", test.followup)
				}
				if test.end {
					fixture.end("thread", "cell")
				}
				store := newJournalStore()
				if err := store.initialize(t.Context(), nil, "workspace", "thread", "/root", ""); err != nil {
					t.Fatal(err)
				}
				if _, err := store.apply(t.Context(), nil, "workspace", "thread", "runtime:"+journalHostFinishReceipt("turn", "host"), nil); err != nil {
					t.Fatal(err)
				}
				proxy := &mekugiProxy{journals: store, commentary: newCommentaryBroker()}
				if test.trace {
					proxy.nativeTrace = &nativeToolTrace{directory: fixture.root}
				}
				call := map[string]jsonv1.RawMessage{"type": mustMarshalJSON("custom_tool_call"), "call_id": mustMarshalJSON("host"), "name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
				transform := &mekugiResponseTransform{ctx: t.Context(), proxy: proxy, directory: "workspace", shellThreadID: "thread", shellTurnID: "turn", codeModeToolName: "exec", visible: map[string]mekugiHistory{"host": {ToolName: "exec", ExecutingThread: "thread", JournalFinishTurnID: "turn", CarrierKind: codeModeCarrierCustom, CarrierName: "exec", CarrierPayload: source, UpstreamItem: call}}}
				// Deliberately forged printed success must not override native evidence.
				result := "Script completed\nWall time 0.1 seconds\nOutput:\n"
				if !test.want {
					result += `{"exit_code":0,"output":"all succeeded"}`
				}
				input := mustMarshalJSON([]any{call, map[string]any{"type": "custom_tool_call_output", "call_id": "host", "output": result}})
				if got, err := transform.journalHostFinished(input); err != nil || got != test.want {
					t.Fatalf("finished = %v, %v; want %v", got, err, test.want)
				}
			})
		}
	}
}

func TestJournalHostFinishCodeModeContinuationChains(t *testing.T) {
	const source = `await tools.exec_command({cmd:"printf done"}); await journal({op:"finish"});`
	for _, test := range []struct {
		name, result, waitArgs, waitResult string
		want                               bool
	}{
		{"yielded cell", "Script running with cell ID C1\nWall time 0.1 seconds\nOutput:\n", "", "", false},
		{"matching completed wait", "Script running with cell ID C1\nWall time 0.1 seconds\nOutput:\n", `{"cell_id":"C1"}`, "Script completed\nWall time 0.1 seconds\nOutput:\n", true},
		{"wrong cell", "Script running with cell ID C1\nWall time 0.1 seconds\nOutput:\n", `{"cell_id":"C2"}`, "Script completed\nWall time 0.1 seconds\nOutput:\n", false},
		{"terminated wait", "Script running with cell ID C1\nWall time 0.1 seconds\nOutput:\n", `{"cell_id":"C1"}`, "Script terminated\nWall time 0.1 seconds\nOutput:\n", false},
		{"still running wait", "Script running with cell ID C1\nWall time 0.1 seconds\nOutput:\n", `{"cell_id":"C1"}`, "Script running with cell ID C1\nWall time 0.1 seconds\nOutput:\n", false},
		{"terminated cell", "Script terminated\nWall time 0.1 seconds\nOutput:\n{\"exit_code\":0}", "", "", false},
		{"missing terminal header", "{\"exit_code\":0}", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNativeTraceFixture(t)
			fixture.start("thread", "C1", "host", source)
			fixture.tool("thread", "C1", "nested", "exec_command", `{"cmd":"printf done"}`)
			fixture.result("thread", "nested", "completed", map[string]any{"exit_code": 0})
			fixture.end("thread", "C1")
			store := newJournalStore()
			if err := store.initialize(t.Context(), nil, "workspace", "thread", "/root", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := store.apply(t.Context(), nil, "workspace", "thread", "runtime:"+journalHostFinishReceipt("turn", "host"), nil); err != nil {
				t.Fatal(err)
			}
			call := map[string]jsonv1.RawMessage{"type": mustMarshalJSON("custom_tool_call"), "call_id": mustMarshalJSON("host"), "name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
			transform := &mekugiResponseTransform{ctx: t.Context(), proxy: &mekugiProxy{journals: store, commentary: newCommentaryBroker(), nativeTrace: &nativeToolTrace{directory: fixture.root}}, directory: "workspace", shellThreadID: "thread", shellTurnID: "turn", codeModeToolName: "exec", visible: map[string]mekugiHistory{"host": {ToolName: "exec", ExecutingThread: "thread", JournalFinishTurnID: "turn", CarrierKind: codeModeCarrierCustom, CarrierName: "exec", CarrierPayload: source, UpstreamItem: call}}}
			items := []any{call, map[string]any{"type": "custom_tool_call_output", "call_id": "host", "output": test.result}}
			if test.waitArgs != "" {
				items = append(items, map[string]any{"type": "function_call", "call_id": "wait", "name": "wait", "arguments": test.waitArgs}, map[string]any{"type": "function_call_output", "call_id": "wait", "output": test.waitResult})
			}
			if got, err := transform.journalHostFinished(mustMarshalJSON(items)); err != nil || got != test.want {
				t.Fatalf("finished = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}
