package launch

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type claudeHookHost struct {
	control.Native
	runtime control.Runtime
}

func (h claudeHookHost) Runtime(string) (control.Runtime, error) { return h.runtime, nil }
func (h claudeHookHost) Resume(context.Context, string) (control.Runtime, error) {
	return h.runtime, nil
}
func (h claudeHookHost) Inspect(ctx context.Context, thread string) (control.SessionDetails, error) {
	return h.runtime.Inspect(ctx, thread)
}

type claudeHookTransport struct {
	request func(context.Context, string, jsontext.Value) (jsontext.Value, error)
}

func (h claudeHookTransport) Request(ctx context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
	return h.request(ctx, method, raw)
}

type claudeHookFixture struct {
	bridge *claudeBridge
	parent history.Session
	mu     sync.Mutex
	inputs []claude.Input
}

func newClaudeHookFixture(t *testing.T) *claudeHookFixture {
	t.Helper()
	ctx := t.Context()
	shared := control.New()
	directory := t.TempDir()
	owners, err := composition.Frontends(ctx, false, false, directory, shared)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owners.Close(context.WithoutCancel(ctx)); err != nil {
			t.Error(err)
		}
	})
	store, err := owners.History.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transaction(ctx, nil, func(tx *history.Transaction) error {
		if err := tx.InitializeScope(history.Scope{Thread: "parent", Namespace: "parent"}, nil); err != nil {
			return err
		}
		return tx.InitializeScope(history.Scope{Thread: "child", Namespace: "parent", Parent: "parent"}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	parent, err := store.SelectSession(ctx, nil, history.Limits{Total: history.DefaultStorageBytes}, "parent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(parent.Close)
	selected, err := store.SelectSession(ctx, nil, parent.Limits, "child")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(selected.Close)
	settings := control.Settings{Workspace: t.TempDir(), Model: "claude:test", ApprovalPolicy: jsontext.Value(`"on-request"`), Preparation: jsontext.Value(`{"config":{},"layers":[]}`), NonAttachable: true}
	policy, err := claude.CompilePolicy(settings.ApprovalPolicy, "", false)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints, err := owners.Changes.Checkpoints(store.Directory())
	if err != nil {
		t.Fatal(err)
	}
	f := &claudeHookFixture{parent: parent}
	b := &claudeBridge{ctx: ctx, info: control.ThreadInfo{Thread: "child", Session: "native-session", Provider: "claude"}, settings: settings, selected: changes.Scope{Store: store, Thread: "child", Namespace: "parent", Limits: parent.Limits}, session: selected,
		owners: owners, checkpoints: checkpoints, windows: &executionobservation.Windows{}, policy: policy, captures: make(map[string]*claudeToolCapture), changes: make(chan []changes.Update, 64)}
	f.bridge = b
	runtime, err := sessioncontrol.NewClaudeRuntime(ctx, selected, b.info, settings, claudeHookTransport{request: func(ctx context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
		if method == "interrupt" {
			f.mu.Lock()
			uuid := f.inputs[len(f.inputs)-1].UUID
			f.mu.Unlock()
			return json.Marshal(map[string]any{"cancelled": []string{uuid}, "still_queued": []string{}})
		}
		if method != "input" {
			return nil, errors.New("unexpected native request: " + method)
		}
		var input claude.Input
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.inputs = append(f.inputs, input)
		f.mu.Unlock()
		replay, _ := json.Marshal(map[string]any{"type": "command_lifecycle", "state": "started", "command_uuid": input.UUID, "session_id": input.SessionID})
		if err := b.event(ctx, replay); err != nil {
			return nil, err
		}
		return jsontext.Value(`{"status":"queued"}`), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	b.runtime = runtime
	runtime.SetEventSink(b.reduceEvent)
	b.info, err = runtime.Thread(ctx, "child")
	if err != nil {
		t.Fatal(err)
	}
	b.execution = &threadExecution{shared: shared, driver: owners.Threads, hub: sessionapi.NewHub(ctx, nil), retained: b.selected}
	audience := sessioncontrol.NewAudience(nil)
	t.Cleanup(audience.Close)
	if err := owners.Threads.Connect(claudeHookHost{runtime: runtime}, audience); err != nil {
		t.Fatal(err)
	}
	if err := shared.AdmitRoot(ctx, parent, &control.Member{Thread: "parent", Session: "parent-session", Task: "/root", Status: control.Working, AudienceAdmitted: true, Settings: control.Settings{Model: "codex", Workspace: settings.Workspace}}); err != nil {
		t.Fatal(err)
	}
	caller := control.Identity{Thread: "parent", Session: "parent-session", Turn: "parent-turn", Call: "spawn", Item: "spawn"}
	if err := shared.RequestStarted(ctx, caller, true); err != nil {
		t.Fatal(err)
	}
	spawn, err := shared.ReserveSpawn(ctx, caller, control.SpawnRequest{Name: "child", Message: "assignment", Settings: settings, Limits: control.Limits{MaxWaitMS: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"create", "assignment_input", "admit"} {
		if _, err := shared.ClaimEffect(ctx, "parent", spawn.ID); err != nil {
			t.Fatal(err)
		}
		if err := shared.EffectAcknowledged(ctx, "parent", spawn.ID, control.Receipt{Phase: phase, Thread: "child", Session: b.info.Session, Path: b.info.Path}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := shared.ClaimEffect(ctx, "parent", spawn.ID); err != nil {
		t.Fatal(err)
	}
	turn, err := runtime.Input(ctx, control.NativeInput{Thread: "child", ID: "initial", Sender: "/root", Recipient: "/root/child", Kind: "NEW_TASK", Message: "assignment", Run: true, Settings: &settings})
	if err != nil {
		t.Fatal(err)
	}
	if err := shared.EffectAcknowledged(ctx, "parent", spawn.ID, control.Receipt{Phase: "initial", Thread: "child", Session: b.info.Session, Path: b.info.Path, Turn: turn}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.discard)
	return f
}

func (f *claudeHookFixture) hook(t *testing.T, event, tool, id string, input, response jsontext.Value) jsontext.Value {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"session_id": f.bridge.info.Session, "hook_event_name": event, "tool_name": tool, "tool_use_id": id, "tool_input": input, "tool_response": response, "error": "native failure"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.bridge.request(t.Context(), "hook", raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestClaudeLaunchHooksPublishSharedNativeEvidence(t *testing.T) {
	f := newClaudeHookFixture(t)
	b := f.bridge
	for _, test := range []struct{ tool, event, path string }{{"Bash", "PostToolUse", "success.txt"}, {"Bash", "PostToolUseFailure", "failed.txt"}} {
		args, _ := json.Marshal(map[string]string{"command": "printf 'observed\\n' > " + test.path})
		f.hook(t, "PreToolUse", test.tool, test.path, args, nil)
		if err := os.WriteFile(filepath.Join(b.settings.Workspace, test.path), []byte("observed\n"), 0600); err != nil {
			t.Fatal(err)
		}
		result := f.hook(t, test.event, test.tool, test.path, args, jsontext.Value(`{"native":"unchanged"}`))
		var response struct {
			Specific struct {
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(result, &response); err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(response.Specific.Context)
		if len(fields) != 1 {
			t.Fatalf("missing native receipt: %s", result)
		}
		parent := b.selected
		parent.Thread = "parent"
		text, err := b.owners.Changes.Service.Read(t.Context(), parent, changes.Query{Workspace: b.settings.Workspace, IDs: []string{fields[0]}})
		if err != nil || !strings.Contains(text, "+observed") {
			t.Fatalf("parent handoff: %q %v", text, err)
		}
		own, err := b.owners.Changes.Service.Read(t.Context(), parent, changes.Query{Workspace: b.settings.Workspace, View: "list"})
		if err != nil || strings.Contains(own, fields[0]) {
			t.Fatalf("child changes leaked into parent implicit list: %q %v", own, err)
		}
	}
	// Acknowledgements and notifications are captured SDK 0.3.312 output.
	for _, background := range []struct{ tool, id, file, args, ack, status string }{
		{"Bash", "background", "background.txt", `{"command":"printf done > background.txt","run_in_background":true}`, `{"backgroundTaskId":"task","stdout":"","stderr":"","interrupted":false}`, "failed"},
		{"Monitor", "monitor", "monitored.txt", `{"description":"watch","timeout_ms":20000,"command":"printf done > monitored.txt"}`, `{"taskId":"b57221pi8","timeoutMs":20000,"persistent":false}`, "completed"},
	} {
		args := jsontext.Value(background.args)
		f.hook(t, "PreToolUse", background.tool, background.id, args, nil)
		ack := f.hook(t, "PostToolUse", background.tool, background.id, args, jsontext.Value(background.ack))
		if !strings.Contains(string(ack), `"additionalContext":""`) {
			t.Fatalf("%s acknowledgement finalized: %s", background.tool, ack)
		}
		if err := os.WriteFile(filepath.Join(b.settings.Workspace, background.file), []byte("done\n"), 0600); err != nil {
			t.Fatal(err)
		}
		notification, _ := json.Marshal(map[string]string{"type": "system", "subtype": "task_notification", "session_id": "native-session", "tool_use_id": background.id, "task_id": "task", "status": background.status, "summary": "background settled after writing"})
		if err := b.event(t.Context(), notification); err != nil {
			t.Fatal(err)
		}
		flushed := f.hook(t, "PostToolBatch", "", "", nil, nil)
		var response struct {
			Specific struct {
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(flushed, &response); err != nil {
			t.Fatal(err)
		}
		ids := strings.Fields(response.Specific.Context)
		if len(ids) != 1 {
			t.Fatalf("expected only capture ID: %s", flushed)
		}
		diff, err := b.owners.Changes.Service.Read(t.Context(), b.selected, changes.Query{Workspace: b.settings.Workspace, IDs: ids})
		if err != nil || !strings.Contains(diff, background.file) || !strings.Contains(diff, "+done") {
			t.Fatalf("settled %s receipt not delivered to next hook: %s", background.tool, flushed)
		}
		b.mu.Lock()
		_, pending := b.captures[background.id]
		b.mu.Unlock()
		if pending {
			t.Fatalf("settled %s capture leaked", background.tool)
		}
	}
	if err := b.execution.AttachChild(t.Context(), "parent", "child"); err == nil {
		t.Fatal("Claude child was attachable")
	}
}

func TestClaudeHookCaptureFailureIsNotCommandFailure(t *testing.T) {
	f := newClaudeHookFixture(t)
	args := jsontext.Value(`{"command":"printf observed > file.txt"}`)
	f.hook(t, "PreToolUse", "Bash", "discarded", args, nil)
	f.bridge.captures["discarded"].operation.Discard()
	result := f.hook(t, "PostToolUse", "Bash", "discarded", args, jsontext.Value(`{"stdout":"command succeeded"}`))
	if !strings.Contains(string(result), "Capture failed: native tool capture was discarded") || strings.Contains(string(result), "command failed") {
		t.Fatalf("capture failure mislabeled: %s", result)
	}
}

func TestClaudeCompletionContextOmitsYieldedEvidenceAtReceiver(t *testing.T) {
	for _, reset := range []string{"none", "before preparation", "before wake"} {
		t.Run(reset, func(t *testing.T) {
			f := newClaudeHookFixture(t)
			b, ctx := f.bridge, t.Context()
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			args := jsontext.Value(`{"command":"printf yielded > captured.txt"}`)
			f.hook(t, "PreToolUse", "Bash", "capture", args, nil)
			check(os.WriteFile(filepath.Join(b.settings.Workspace, "captured.txt"), []byte("yielded\n"), 0600))
			f.hook(t, "PostToolUse", "Bash", "capture", args, jsontext.Value(`{"stdout":"","stderr":"","interrupted":false}`))
			index, err := b.owners.Changes.Service.Inspect(b.selected, b.settings.Workspace)
			check(err)
			var id string
			seen := make(map[string]int)
			for changeID, change := range index.Changes {
				id = changeID
				for _, call := range change.Calls {
					record, found, err := b.selected.Store.ReadHostCall(b.settings.Workspace, call.ID, false)
					check(err)
					if !found {
						t.Fatal("missing capture")
					}
					seen[changes.CaptureKey(b.settings.Workspace, record.History.Caller, id, record.CaptureOrder)] = 1
				}
			}
			if len(seen) == 0 {
				t.Fatal("missing captured progress")
			}
			shared := b.execution.shared
			caller, err := b.identity("spawn-nested")
			check(err)
			spawn, err := shared.ReserveSpawn(ctx, caller, control.SpawnRequest{Name: "nested", Message: "work", Settings: b.settings})
			check(err)
			for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
				_, err = shared.ClaimEffect(ctx, "child", spawn.ID)
				check(err)
				check(shared.EffectAcknowledged(ctx, "child", spawn.ID, control.Receipt{Phase: phase, Thread: "nested", Session: "nested-session", Turn: "nested-turn"}))
			}
			caller.Call, caller.Item = "yield", "yield-item"
			now := time.Now()
			_, err = shared.RegisterWait(ctx, caller, control.WaitAny, nil, new(int64(300000)), now)
			check(err)
			check(shared.Execution(ctx, "child", caller.Turn, caller.Item, "", true))
			stop, err := shared.ClaimLocalStop(ctx, "child", caller.Turn, caller.Item)
			check(err)
			if stop == nil {
				t.Fatal("yield was not admitted")
			}
			check(shared.HostCompleted(ctx, "child", caller.Turn, control.Completed, control.HostFailure{}))
			_, err = shared.PrepareWake(ctx, "child", now.Add(5*time.Minute))
			check(err)
			_, err = shared.ClaimEffect(ctx, "child", caller.Call)
			check(err)
			check(shared.RetainWaitProgress(ctx, "child", caller.Call, b.runtime.ContextIdentity("child"), control.WaitProgress{}, seen))
			check(shared.EffectAcknowledged(ctx, "child", caller.Call, control.Receipt{Turn: caller.Turn}))
			compactContext := func() {
				check(b.runtime.ObserveMetrics(ctx, jsontext.Value(`{"type":"system","subtype":"compact_boundary","session_id":"native-session","uuid":"replacement"}`)))
			}
			if reset == "before preparation" {
				compactContext()
			}
			b.runtime.SetInputContext(b.completionContext)
			selection, err := json.Marshal(map[string]any{"sources": []compact.Source{{Changes: []string{id}}}})
			check(err)
			_, err = b.runtime.Input(ctx, control.NativeInput{Thread: "child", ID: "completion", Kind: "FINAL_ANSWER", Outcome: control.Completed, Sender: "/root/child/nested", Message: "Done\n" + compact.CompactMetadataStart + string(selection) + compact.CompactMetadataEnd})
			check(err)
			if reset == "before wake" {
				compactContext()
			}
			_, err = b.runtime.Input(ctx, control.NativeInput{Thread: "child", ID: "wake", Kind: "MESSAGE", Run: true})
			check(err)
			f.mu.Lock()
			text := string(f.inputs[len(f.inputs)-1].Message)
			f.mu.Unlock()
			if strings.Contains(text, "+yielded") != (reset != "none") {
				t.Fatalf("wrong receiving context attachment: %s", text)
			}
			full, err := b.owners.Changes.Service.Read(ctx, b.selected, changes.Query{Workspace: b.settings.Workspace, IDs: []string{id}, View: "net"})
			check(err)
			if !strings.Contains(full, "+yielded") {
				t.Fatal("explicit read lost yielded capture")
			}
		})
	}
}

func TestClaudeLaunchWaitStopsOnlyOnSuccessfulBatch(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failed sibling"}[failed], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newClaudeHookFixture(t)
				b := f.bridge
				caller, _ := b.identity("spawn-grandchild")
				spawn, err := b.execution.shared.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: "worker", Message: "work", Settings: b.settings})
				if err != nil {
					t.Fatal(err)
				}
				for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
					if _, err := b.execution.shared.ClaimEffect(t.Context(), "child", spawn.ID); err != nil {
						t.Fatal(err)
					}
					if err := b.execution.shared.EffectAcknowledged(t.Context(), "child", spawn.ID, control.Receipt{Phase: phase, Thread: "grandchild", Session: "grandchild-session", Turn: "grandchild-turn"}); err != nil {
						t.Fatal(err)
					}
				}
				f.hook(t, "PreToolUse", "mcp__threadcontrol__wait_agent", "wait", jsontext.Value(`{}`), nil)
				tool, err := b.request(t.Context(), "tool", jsontext.Value(`{"name":"wait_agent","toolUseID":"wait","arguments":{}}`))
				if err != nil {
					t.Fatal(err)
				}
				f.hook(t, "PostToolUse", "mcp__threadcontrol__wait_agent", "wait", jsontext.Value(`{}`), tool)
				var nativeResult struct {
					Content jsontext.Value `json:"content"`
				}
				if err := json.Unmarshal(tool, &nativeResult); err != nil {
					t.Fatal(err)
				}
				calls := []map[string]any{{"tool_name": "mcp__threadcontrol__wait_agent", "tool_use_id": "wait", "tool_response": nativeResult.Content}}
				if failed {
					f.hook(t, "PreToolUse", "Read", "read", jsontext.Value(`{"file_path":"missing"}`), nil)
					f.hook(t, "PostToolUseFailure", "Read", "read", jsontext.Value(`{"file_path":"missing"}`), nil)
					calls = append(calls, map[string]any{"tool_name": "Read", "tool_use_id": "read"})
				}
				raw, _ := json.Marshal(map[string]any{"session_id": b.info.Session, "hook_event_name": "PostToolBatch", "tool_calls": calls})
				result, err := b.request(t.Context(), "hook", raw)
				if err != nil {
					t.Fatal(err)
				}
				stopped := strings.Contains(string(result), `"continue":false`)
				if stopped == failed {
					t.Fatalf("batch stop did not reflect native success: %s", result)
				}
				member, _ := b.execution.shared.Member("child")
				if !member.HostRunning {
					t.Fatal("hook request settled host before native terminal")
				}
				if failed {
					return
				}
				f.mu.Lock()
				uuid := f.inputs[0].UUID
				f.mu.Unlock()
				terminal, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": b.info.Session, "uuid": "terminal", "user_message_uuid": uuid, "terminal_reason": "hook_stopped"})
				if err := b.event(t.Context(), terminal); err != nil {
					t.Fatal(err)
				}
				select {
				case <-b.execution.driver.Changed():
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				}
				member, _ = b.execution.shared.Member("child")
				if member.HostRunning || member.Status != control.Waiting {
					t.Fatalf("native hook stop lost logical wait: %+v", member)
				}
				if err := b.runtime.ObserveLogicalWork(t.Context(), member, time.Now()); err != nil {
					t.Fatal(err)
				}
				waiting, _ := b.runtime.Metrics()
				if waiting.WorkSince.IsZero() {
					t.Fatal("native hook stop froze logical waiting time")
				}
				time.Sleep(10 * time.Second)
				if err := b.execution.shared.AnswerCompleted(t.Context(), "grandchild", "grandchild-turn", "answer", "finished child task"); err != nil {
					t.Fatal(err)
				}
				b.execution.driver.Completed("grandchild", "grandchild-turn", control.Completed, control.HostFailure{}, nil)
				select {
				case <-b.execution.driver.Changed():
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				}
				f.mu.Lock()
				inputs := append([]claude.Input(nil), f.inputs...)
				f.mu.Unlock()
				member, _ = b.execution.shared.Member("child")
				if len(inputs) != 2 || inputs[1].ShouldQuery != nil && !*inputs[1].ShouldQuery || !strings.Contains(string(inputs[1].Message), "finished child task") || !member.HostRunning || member.Status != control.Working {
					t.Fatalf("native wake lost ordered child result or active state: inputs=%+v member=%+v", inputs, member)
				}
				if err := b.runtime.ObserveLogicalWork(t.Context(), member, time.Now()); err != nil {
					t.Fatal(err)
				}
				woken, _ := b.runtime.Metrics()
				if woken.WorkElapsed != waiting.WorkElapsed+10*time.Second || woken.WorkSince.IsZero() {
					t.Fatalf("wake lost waiting interval: waiting=%+v woken=%+v", waiting, woken)
				}
			})
		})
	}
}

func TestClaudeLaunchApprovalAndRetainedInspection(t *testing.T) {
	f := newClaudeHookFixture(t)
	b := f.bridge
	b.execution.approvals = sessioncontrol.NewDelegateApprovals(t.Context())
	defer b.execution.approvals.Close()
	result := make(chan error, 1)
	go func() {
		_, err := b.request(t.Context(), "permission", jsontext.Value(`{"name":"Bash","toolUseID":"approved","arguments":{"command":"printf approval"}}`))
		result <- err
	}()
	request := <-b.execution.approvals.Requests()
	if request.Approval.Thread != b.info.Thread || request.Approval.Item != "approved" || request.Approval.Subject != "Bash" {
		t.Fatalf("approval lost admitted identity: %+v", request.Approval)
	}
	request.Reply(prompt.Choice{Approve: true}, "")
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := b.event(t.Context(), jsontext.Value(`{"type":"system","subtype":"init","session_id":"native-session","model":"observed-model"}`)); err != nil {
		t.Fatal(err)
	}
	details, err := b.execution.Inspect(t.Context(), "child")
	if err != nil || details.Model == nil || *details.Model != "observed-model" || details.ContextTokens != nil || details.ContextWindow != nil {
		t.Fatalf("inactive native inspection fabricated or lost facts: %+v %v", details, err)
	}
	input := control.NativeInput{Thread: "child", ID: "initial", Sender: "/root", Recipient: "/root/child", Kind: "NEW_TASK", Message: "assignment", Run: true, Settings: &b.settings}
	if err := b.execution.Confirm(t.Context(), input); err != nil {
		t.Fatalf("inactive confirmation lost accepted native input: %v", err)
	}
	retained, err := sessioncontrol.ReadClaudeThread(t.Context(), "child", b.info.Path)
	if err != nil || retained.Session != b.info.Session || retained.Path != b.info.Path {
		t.Fatalf("resume lost exact SDK identity: %+v %v", retained, err)
	}
}

type claudeJoinedWriter struct {
	command *exec.Cmd
	input   io.Closer
}

func TestClaudeContextWarningSharesToolBatchMilestone(t *testing.T) {
	b := newClaudeHookFixture(t).bridge
	// Existing SDK/worker event shapes, with usage at the 60% milestone.
	for _, raw := range []string{
		`{"type":"system","subtype":"init","session_id":"native-session","model":"native-model"}`,
		`{"type":"assistant","session_id":"native-session","uuid":"block-1","message":{"id":"msg-1"},"parent_tool_use_id":null}`,
		`{"type":"mekugi_live_metrics","session_id":"native-session","message_id":"msg-1","context_tokens":120000,"context_window":200000,"modelUsage":null,"total_cost_usd":null}`,
	} {
		if err := b.event(t.Context(), jsontext.Value(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, wantReminder := range []bool{true, false} {
		result, err := b.batch(t.Context(), claudeHook{Event: "PostToolUse"})
		if err != nil || strings.Contains(string(result), "Mekugi context usage: 60%") != wantReminder {
			t.Fatalf("tool batch milestone: %s, %v", result, err)
		}
	}
	pending := b.owners.Notifications.Pending()
	if len(pending) != 1 || !strings.Contains(pending[0], "Mekugi context usage: 60% (120000/200000 tokens).") || strings.Contains(pending[0], "occurred") {
		t.Fatalf("user warning missing or duplicated: %v", pending)
	}
	if claim := b.owners.Notifications.Claim(b.info.Thread, nil); claim == nil || len(claim.Errors.Messages) != 0 || len(claim.Progress.Messages) != 1 || claim.Progress.Messages[0].ID == "" {
		t.Fatalf("context milestone is not an identified progress event: %+v", claim)
	}
	if err := b.event(t.Context(), jsontext.Value(`{"type":"mekugi_live_metrics","session_id":"native-session","message_id":"msg-1","context_tokens":180000,"context_window":200000,"modelUsage":null,"total_cost_usd":null}`)); err != nil {
		t.Fatal(err)
	}
	for _, wantReminder := range []bool{true, false} {
		result, err := b.batch(t.Context(), claudeHook{Event: "PostToolUse"})
		if err != nil || strings.Contains(string(result), "Mekugi context usage: 90% (180000/200000 tokens). You must compact now.") != wantReminder {
			t.Fatalf("urgent tool batch milestone: %s, %v", result, err)
		}
	}
}

func TestClaudeLaunchInterruptReceiptReducesTerminal(t *testing.T) {
	f := newClaudeHookFixture(t)
	b := f.bridge
	turn := b.runtime.CurrentTurn()
	if err := b.runtime.Interrupt(t.Context(), b.info.Thread, turn); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.execution.driver.Changed():
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	member, _ := b.execution.shared.Member(b.info.Thread)
	if member.HostRunning || member.Status != control.Interrupted || b.runtime.CurrentTurn() != "" {
		t.Fatalf("interrupt-only terminal receipt was not reduced: %+v", member)
	}
}

func (writer claudeJoinedWriter) Close() error {
	return errors.Join(writer.input.Close(), writer.command.Wait())
}

func TestClaudeLaunchJoinedCloseCapturesUnknownOutcome(t *testing.T) {
	f := newClaudeHookFixture(t)
	b := f.bridge
	path := filepath.Join(b.settings.Workspace, "joined.txt")
	if err := os.WriteFile(path, []byte("before close\n"), 0600); err != nil {
		t.Fatal(err)
	}
	toolCtx, cancel := context.WithCancel(t.Context())
	pre := jsontext.Value(`{"session_id":"native-session","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"joined","tool_input":{"command":"printf 'after joined close\\n' > joined.txt"}}`)
	if _, err := b.request(toolCtx, "hook", pre); err != nil {
		t.Fatal(err)
	}
	cancel()
	writerCtx, stopWriter := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopWriter()
	// EOF requests cleanup without depending on inherited signal state. The
	// deadline also bounds startup and joining if the fixture stops responding.
	command := exec.CommandContext(writerCtx, execTrackShellExecutable(t, "bash"), "-c", "printf 'ready\\n'; read -r ignored; printf 'after joined close\\n' > joined.txt")
	command.Dir = b.settings.Workspace
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	if ready, err := bufio.NewReader(output).ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatalf("writer startup: %q %v", ready, err)
	}
	// The native writer's effect happens during process cleanup. No terminal
	// hook is supplied, and the pre-tool request context has already ended.
	if err := b.close(claudeJoinedWriter{command: command, input: input}); err != nil {
		t.Fatal(err)
	}
	if command.ProcessState == nil {
		t.Fatal("capture completed before native process joined")
	}
	retained, found, err := b.selected.Store.LookupHostCall(t.Context(), b.settings.Workspace, legacycall.ExecCallID("claude:child:joined", false))
	if err != nil || !found || retained.ChangeID == "" || retained.ExecOutcome == nil {
		t.Fatalf("joined capture unavailable: %+v %v", retained, err)
	}
	if retained.ExecOutcome.Status != "" || retained.ExecOutcome.Exit != nil || len(retained.HostResults) != 1 || retained.HostResults[0].Status != "" || retained.HostResults[0].ExitCode != nil {
		t.Fatalf("missing native hook invented an outcome: %+v", retained)
	}
	diff, err := b.owners.Changes.Service.Read(t.Context(), b.selected, changes.Query{Workspace: b.settings.Workspace, IDs: []string{retained.ChangeID}})
	if err != nil || !strings.Contains(diff, "-before close\n+after joined close") {
		t.Fatalf("cleanup effect was lost or captured before join: %q %v", diff, err)
	}
	diagnostic, err := b.owners.Changes.Service.Read(t.Context(), b.selected, changes.Query{Workspace: b.settings.Workspace, IDs: []string{retained.ChangeID}, View: "history"})
	if err != nil || !strings.Contains(diagnostic, "native tool outcome unavailable") || strings.Contains(diagnostic, "command failed") || strings.Contains(diagnostic, "command completed") {
		t.Fatalf("missing outcome diagnostic: %q %v", diagnostic, err)
	}
	if len(b.captures) != 0 {
		t.Fatal("joined capture windows were not retired")
	}
}
