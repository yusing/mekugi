package sessioncontrol

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type claudeRequestFunc func(context.Context, string, jsontext.Value) (jsontext.Value, error)

func (fn claudeRequestFunc) Request(ctx context.Context, method string, params jsontext.Value) (jsontext.Value, error) {
	return fn(ctx, method, params)
}

func claudeFixture(t *testing.T, request ClaudeRequester) (*ClaudeRuntime, history.Session, control.Settings) {
	t.Helper()
	kinds := append(ClaudeRecordKinds(), recordkind.Spec{ID: "scope", Match: func(name string) bool { return strings.HasPrefix(name, "handle-scope-") }})
	service, err := history.NewService(kinds)
	if err != nil {
		t.Fatal(err)
	}
	store, err := service.Open(t.Context(), filepath.Join(t.TempDir(), "history"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		if err := tx.WriteScope(history.Scope{Version: 1, Thread: "root", Namespace: "root"}); err != nil {
			return err
		}
		return tx.WriteScope(history.Scope{Version: 1, Thread: "child", Namespace: "root", Parent: "root"})
	}); err != nil {
		t.Fatal(err)
	}
	selected, err := store.SelectSession(t.Context(), nil, history.Limits{}, "child")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(selected.Close)
	settings := control.Settings{Workspace: t.TempDir(), Model: "claude:test", Preparation: jsontext.Value(`{"model":"test","permissionMode":"default"}`)}
	runtime, err := NewClaudeRuntime(t.Context(), selected, control.ThreadInfo{Thread: "child", Session: "native-session", Parent: new("root")}, settings, request)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, selected, settings
}

func claudeStarted(t *testing.T, runtime *ClaudeRuntime, input claude.Input) ClaudeEvent {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"type": "command_lifecycle", "state": "started", "command_uuid": input.UUID, "uuid": "event-" + input.UUID, "session_id": input.SessionID})
	event, err := runtime.HandleEvent(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestClaudeRetainedInputStaysInHostLedgerUntilQuery(t *testing.T) {
	var runtime *ClaudeRuntime
	input := control.NativeInput{Thread: "child", ID: "message-1", Sender: "/root", Recipient: "/root/child", Kind: "MESSAGE", Message: "Retained agent message"}
	requests := 0
	native := claudeRequestFunc(func(context.Context, string, jsontext.Value) (jsontext.Value, error) {
		requests++
		return nil, errors.New("retained-only input must not use native IPC")
	})
	runtime, selected, settings := claudeFixture(t, native)
	if turn, err := runtime.Input(t.Context(), input); err != nil || turn != "" || runtime.CurrentTurn() != "" {
		t.Fatalf("retained input: turn=%q err=%v", turn, err)
	}
	if err := runtime.Confirm(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	var receipt claudeReceipt
	if err := runtime.read(claudeRecordName(input.Thread, "input", input.ID), &receipt); err != nil || receipt.Input == nil || !receipt.Accepted || receipt.Input.Message != input.Message {
		t.Fatalf("retained-only payload was dropped before querying consumption: %+v %v", receipt, err)
	}
	for _, raw := range []jsontext.Value{jsontext.Value(`{"type":"system","subtype":"command_lifecycle","session_id":"native-session","status":"completed"}`), jsontext.Value(`{"type":"result","subtype":"success","session_id":"native-session","uuid":"ephemeral","result":""}`)} {
		if event, err := runtime.HandleEvent(t.Context(), raw); err != nil || event.Started || event.Completed {
			t.Fatalf("nonquery lifecycle invented a work turn: %+v %v", event, err)
		}
	}
	info, _ := runtime.Thread(t.Context(), "child")
	retainedInfo, err := ReadClaudeThread(t.Context(), "child", info.Path)
	if err != nil || retainedInfo.Session != info.Session || retainedInfo.Parent == nil || *retainedInfo.Parent != "root" {
		t.Fatalf("inactive native identity: %+v %v", retainedInfo, err)
	}
	if err := ConfirmClaudeInput(t.Context(), info.Path, input); err != nil {
		t.Fatalf("inactive confirmation: %v", err)
	}
	reopened, err := NewClaudeRuntime(t.Context(), selected, info, settings, nil)
	if err != nil || reopened.Confirm(t.Context(), input) != nil {
		t.Fatalf("durable exact confirmation: %v", err)
	}
	changed := input
	changed.Message = "same operation, different payload"
	if err := reopened.Confirm(t.Context(), changed); err == nil {
		t.Fatal("different input confirmed")
	}
	configuration, err := reopened.Configuration(t.Context(), "child", settings.Workspace)
	if err != nil || !sameNativeJSON(configuration, settings.Preparation) {
		t.Fatalf("frozen configuration: %s %v", configuration, err)
	}
	configuration[0] = '['
	configuration, _ = reopened.Configuration(t.Context(), "child", settings.Workspace)
	if !configuration.IsValid() {
		t.Fatal("caller mutated frozen preparation")
	}
	details, _ := reopened.Inspect(t.Context(), "child")
	if details.Model != nil || details.ContextTokens != nil || details.ContextWindow != nil || details.Compactions != nil || details.ReasoningEffort != nil || details.ServiceTier != nil {
		t.Fatalf("unobserved metrics were fabricated: %+v", details)
	}
	if _, err := reopened.HandleEvent(t.Context(), jsontext.Value(`{"type":"system","subtype":"init","session_id":"native-session","model":"native-model"}`)); err != nil {
		t.Fatal(err)
	}
	details, _ = reopened.Inspect(t.Context(), "child")
	if details.Model == nil || *details.Model != "native-model" {
		t.Fatal("native model was not observed")
	}
	details, err = ReadClaudeDetails(t.Context(), "child", info.Path)
	if err != nil || details.Model == nil || *details.Model != "native-model" || details.ContextTokens != nil {
		t.Fatalf("inactive inspection fabricated or lost native facts: %+v %v", details, err)
	}
	if _, err := ReadClaudeDetails(t.Context(), "other", info.Path); err == nil {
		t.Fatal("inactive inspection admitted another thread")
	}
	if requests != 0 {
		t.Fatalf("confirmation issued %d native requests", requests)
	}
}

func TestClaudeQueuedInputIsUncertainAndRecoveryNeverResends(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var queued claude.Input
	dispatches := 0
	native := claudeRequestFunc(func(_ context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
		dispatches++
		if err := json.Unmarshal(raw, &queued); err != nil {
			t.Fatal(err)
		}
		cancel()
		return jsontext.Value(`{"status":"queued"}`), nil
	})
	runtime, selected, settings := claudeFixture(t, native)
	input := control.NativeInput{Thread: "child", ID: "followup", Sender: "/root", Recipient: "/root/child", Kind: "NEW_TASK", Message: "work", Run: true}
	_, err := runtime.Input(ctx, input)
	failure, ok := errors.AsType[*binding.DispatchError](err)
	if !ok || failure.Outcome != binding.Uncertain {
		t.Fatalf("queued input claimed acceptance: %v", err)
	}
	if _, err := runtime.Input(t.Context(), input); err == nil || dispatches != 1 {
		t.Fatalf("uncertain input redispatched: %d %v", dispatches, err)
	}
	info, _ := runtime.Thread(t.Context(), "child")
	if err := ConfirmClaudeInput(t.Context(), info.Path, input); !errors.Is(err, errClaudeAcceptance) {
		t.Fatalf("inactive queued receipt claimed acceptance: %v", err)
	}
	dependencies, err := selected.Store.Dependencies(filepath.Base(info.Path))
	if err != nil || len(dependencies) != 1 || dependencies[0] != claudeRecordName(input.Thread, "input", input.ID) {
		t.Fatalf("pending input dependency missing: %v %v", dependencies, err)
	}
	wrong := info
	wrong.Session = "different-native-session"
	if _, err := NewClaudeRuntime(t.Context(), selected, wrong, settings, native); err == nil {
		t.Fatal("resume accepted a different native session")
	}
	reopened, err := NewClaudeRuntime(t.Context(), selected, info, settings, native)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Confirm(t.Context(), input); !errors.Is(err, errClaudeAcceptance) || reopened.CurrentTurn() != "" || dispatches != 1 {
		t.Fatalf("uncertain recovery queried or restarted native work: %v", err)
	}
	claudeStarted(t, runtime, queued)
	if err := reopened.Confirm(t.Context(), input); err != nil || reopened.CurrentTurn() != "" || dispatches != 1 {
		t.Fatalf("exact started receipt did not survive restart: dispatches=%d err=%v", dispatches, err)
	}
}

func TestClaudeRunningInputInterruptionAndTerminalReceipts(t *testing.T) {
	var runtime *ClaudeRuntime
	var inputs []claude.Input
	var events []ClaudeEvent
	interrupts := 0
	native := claudeRequestFunc(func(_ context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
		if method == "interrupt" {
			interrupts++
			return jsontext.Value(`{}`), nil
		}
		var input claude.Input
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if !input.IsSynthetic || input.SessionID != "native-session" || input.ShouldQuery != nil && !*input.ShouldQuery || strings.Contains(string(input.Origin), "human") {
			t.Fatalf("invalid native carrier: %+v", input)
		}
		if len(inputs) == 0 {
			var pending claudeReceipt
			if err := runtime.read(claudeRecordName("child", "input", "start"), &pending); err != nil || pending.Input == nil || pending.Accepted || pending.UUID != input.UUID || len(pending.Bundled) != 1 || !strings.Contains(string(input.Message), "retained") {
				t.Fatalf("carrier intent/bundled input missing before IPC: %+v %v", pending, err)
			}
		}
		inputs = append(inputs, input)
		events = append(events, claudeStarted(t, runtime, input))
		return jsontext.Value(`{"status":"queued"}`), nil
	})
	runtime, selected, settings := claudeFixture(t, native)
	message := control.NativeInput{Thread: "child", ID: "message", Sender: "/root", Recipient: "/root/child", Kind: "MESSAGE", Message: "retained"}
	if _, err := runtime.Input(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	start := control.NativeInput{Thread: "child", ID: "start", Sender: "/root", Recipient: "/root/child", Kind: "NEW_TASK", Message: "start", Run: true}
	turn, err := runtime.Input(t.Context(), start)
	if err != nil || turn == "" || !events[0].Started || len(events[0].Consumed) != 0 {
		t.Fatalf("native start claimed consumption: turn=%q event=%+v err=%v", turn, events[0], err)
	}
	var consumed claudeReceipt
	if err := runtime.read(claudeRecordName("child", "input", message.ID), &consumed); err != nil || consumed.Input == nil {
		t.Fatalf("started acknowledgement dropped unconsumed payload: %+v %v", consumed, err)
	}
	followup := start
	followup.ID, followup.Message = "followup", "steer"
	if steered, err := runtime.Input(t.Context(), followup); err != nil || steered != turn || events[1].Started {
		t.Fatalf("followup created competing turn: %q %+v %v", steered, events[1], err)
	}
	if inputs[0].Priority != "" || inputs[1].Priority != "now" {
		t.Fatalf("active followup did not select native steering: initial=%q followup=%q", inputs[0].Priority, inputs[1].Priority)
	}
	interrupt := control.NativeInput{Thread: "child", Kind: "INTERRUPT", TargetTurn: turn}
	if runtime.Confirm(t.Context(), interrupt) == nil || runtime.Interrupt(t.Context(), "child", "other-turn") == nil {
		t.Fatal("unobserved/wrong-turn interruption confirmed")
	}
	if err := runtime.Interrupt(t.Context(), "child", turn); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Interrupt(t.Context(), "child", turn); err != nil || interrupts != 1 || runtime.CurrentTurn() != turn {
		t.Fatalf("interrupt ack claimed terminal or retried: count=%d err=%v", interrupts, err)
	}
	terminal, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": "native-session", "uuid": "terminal-1", "terminal_reason": "hook_stopped", "user_message_uuid": inputs[1].UUID, "user_message_uuids": []string{inputs[0].UUID, inputs[1].UUID}})
	event, err := runtime.HandleEvent(t.Context(), terminal)
	if err != nil || !event.Completed || event.Status != control.Completed || event.TerminalReason != "hook_stopped" || event.Turn != turn || event.Answer != "" || runtime.CurrentTurn() != "" || len(event.Consumed) != 1 || event.Consumed[0] != control.MessageItemID(message.ID) {
		t.Fatalf("hook stop receipt: %+v %v", event, err)
	}
	consumed = claudeReceipt{}
	if err := runtime.read(claudeRecordName("child", "input", message.ID), &consumed); err != nil || consumed.Input != nil || !consumed.Consumed {
		t.Fatalf("consumed payload duplicated native input: %+v %v", consumed, err)
	}
	info, _ := runtime.Thread(t.Context(), "child")
	reopened, err := NewClaudeRuntime(t.Context(), selected, info, settings, nil)
	if err != nil || reopened.Confirm(t.Context(), interrupt) != nil {
		t.Fatalf("terminal receipt did not survive restart: %v", err)
	}
	if err := ConfirmClaudeInput(t.Context(), info.Path, interrupt); err != nil {
		t.Fatalf("inactive exact-turn confirmation: %v", err)
	}
	start.ID = "next"
	if _, err := runtime.Input(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.HandleEvent(t.Context(), terminal); err == nil || runtime.CurrentTurn() == "" {
		t.Fatal("old native terminal settled a newer turn")
	}
	failed, _ := json.Marshal(map[string]any{"type": "result", "subtype": "error_during_execution", "session_id": "native-session", "uuid": "terminal-2", "terminal_reason": "api_error", "is_error": true, "api_error_status": 503, "errors": []string{"unavailable"}, "user_message_uuid": inputs[2].UUID})
	event, err = runtime.HandleEvent(t.Context(), failed)
	if err != nil || event.Status != control.Failed || !event.Failure.Recoverable || event.Failure.Message != "unavailable" {
		t.Fatalf("native failure classified as success: %+v %v", event, err)
	}
}

func TestClaudeWakePreservesAttributionWithoutHumanOrToolResult(t *testing.T) {
	var runtime *ClaudeRuntime
	native := claudeRequestFunc(func(_ context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
		var wire claude.Input
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		var message struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(wire.Message, &message); err != nil {
			t.Fatal(err)
		}
		if !wire.IsSynthetic || wire.ShouldQuery != nil && !*wire.ShouldQuery || string(wire.Origin) != `{"kind":"coordinator"}` || strings.Contains(message.Content, "private-operation") || strings.Contains(message.Content, "tool_result") || !strings.Contains(message.Content, "Thread Control wait_agent\nSender: agent:main") || !strings.Contains(message.Content, `"task":"agent:main/child"`) || !strings.Contains(message.Content, "literal /root/file") {
			t.Fatalf("wake lost attribution or acquired human/tool authority: %+v %s", wire, message.Content)
		}
		claudeStarted(t, runtime, wire)
		return jsontext.Value(`{"status":"queued"}`), nil
	})
	runtime, _, _ = claudeFixture(t, native)
	wake := &control.WaitResult{ID: "private-operation", State: "completed", Targets: []string{"/root/child"}, Event: &control.Event{Task: "/root/child", Message: "literal /root/file"}}
	if _, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "private-operation", Sender: "/root", Recipient: "/root/child", Run: true, Wake: wake}); err != nil {
		t.Fatal(err)
	}
	if wake.Targets[0] != "/root/child" || wake.Event.Task != "/root/child" {
		t.Fatal("provider projection mutated retained wake identity")
	}
}

func TestClaudeStreamingEventsDoNotReadNativeHistory(t *testing.T) {
	requests := 0
	runtime, _, _ := claudeFixture(t, claudeRequestFunc(func(context.Context, string, jsontext.Value) (jsontext.Value, error) {
		requests++
		return nil, errors.New("unexpected native request")
	}))
	for range 1000 {
		if _, err := runtime.HandleEvent(t.Context(), jsontext.Value(`{"type":"stream_event","session_id":"native-session","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}}`)); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 0 {
		t.Fatalf("streaming events rescanned native history %d times", requests)
	}
}

func TestClaudeAcceptanceWaitAndTransportLoss(t *testing.T) {
	for _, closeTransport := range []bool{false, true} {
		t.Run(fmt.Sprint(closeTransport), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var queued claude.Input
				runtime, _, _ := claudeFixture(t, claudeRequestFunc(func(_ context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
					if err := json.Unmarshal(raw, &queued); err != nil {
						return nil, err
					}
					return jsontext.Value(`{"status":"queued"}`), nil
				}))
				done := make(chan error, 1)
				go func() {
					_, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "queued", Kind: "NEW_TASK", Sender: "/root", Message: "work", Run: true})
					done <- err
				}()
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("queue acknowledgement settled input: %v", err)
				default:
				}
				if closeTransport {
					runtime.TransportClosed(errors.New("native worker lost"))
					failure, ok := errors.AsType[*binding.DispatchError](<-done)
					if !ok || failure.Outcome != binding.Uncertain {
						t.Fatalf("transport loss claimed settlement: %v", failure)
					}
				} else {
					claudeStarted(t, runtime, queued)
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
			})
		})
	}
}

func TestClaudeInitialContextSurvivesStartedUntilNativeConsumption(t *testing.T) {
	var runtime *ClaudeRuntime
	var wire claude.Input
	requests, acquisitions := 0, 0
	native := claudeRequestFunc(func(_ context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
		requests++
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(wire.Message), "frozen source body") {
			t.Fatal("initial carrier omitted prepared context")
		}
		claudeStarted(t, runtime, wire)
		return jsontext.Value(`{"status":"queued"}`), nil
	})
	runtime, selected, settings := claudeFixture(t, native)
	runtime.SetInputContext(func(_ context.Context, input control.NativeInput) (string, error) {
		acquisitions++
		if input.ID != "initial" || input.Message != "original assignment" {
			t.Fatalf("context callback changed input authority: %+v", input)
		}
		return "frozen source body", nil
	})
	input := control.NativeInput{Thread: "child", ID: "initial", Sender: "/root", Kind: "NEW_TASK", Message: "original assignment", Run: true, Settings: &settings}
	if _, err := runtime.Input(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	var receipt claudeReceipt
	name := claudeRecordName(input.Thread, "input", input.ID)
	if err := runtime.read(name, &receipt); err != nil || !receipt.Accepted || receipt.Consumed || receipt.Input == nil || receipt.Context != "frozen source body" {
		t.Fatalf("started receipt lost unconsumed context: %+v %v", receipt, err)
	}
	if _, err := runtime.Input(t.Context(), input); err != nil || requests != 1 || acquisitions != 1 {
		t.Fatalf("accepted operation reread sources or redispatched: reads=%d requests=%d err=%v", acquisitions, requests, err)
	}
	info, _ := runtime.Thread(t.Context(), "child")
	reopened, err := NewClaudeRuntime(t.Context(), selected, info, settings, nil)
	if err != nil || reopened.Confirm(t.Context(), input) != nil || reopened.CurrentTurn() != "" {
		t.Fatalf("restart lost exact accepted-start receipt: %v", err)
	}
	terminal, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": wire.SessionID, "uuid": "terminal", "user_message_uuid": wire.UUID, "terminal_reason": "completed", "result": "done"})
	if event, err := runtime.HandleEvent(t.Context(), terminal); err != nil || !event.Completed || event.Answer != "done" {
		t.Fatalf("native consumption: %+v %v", event, err)
	}
	receipt = claudeReceipt{}
	if err := runtime.read(name, &receipt); err != nil || receipt.Input != nil || receipt.Context != "" || !receipt.Consumed {
		t.Fatalf("consumed context duplicated native transcript: %+v %v", receipt, err)
	}
}

func TestClaudeNativeErrorBeforeStartedSettlesExactInput(t *testing.T) {
	var runtime *ClaudeRuntime
	var observed ClaudeEvent
	native := claudeRequestFunc(func(_ context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
		var wire claude.Input
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		terminal, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": wire.SessionID, "uuid": "rejected-terminal", "user_message_uuid": wire.UUID, "terminal_reason": "api_error", "is_error": true, "api_error_status": 401, "errors": []string{"authentication failed"}})
		var err error
		observed, err = runtime.HandleEvent(t.Context(), terminal)
		return jsontext.Value(`{"status":"queued"}`), err
	})
	runtime, _, _ = claudeFixture(t, native)
	if turn, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "rejected", Kind: "NEW_TASK", Message: "work", Run: true}); err != nil || turn == "" || !observed.Started || !observed.Completed || observed.Status != control.Failed || observed.Failure.Recoverable || observed.Failure.Message != "authentication failed" {
		t.Fatalf("native error left input awaiting nonexistent replay: turn=%q event=%+v err=%v", turn, observed, err)
	}
}

func TestClaudePriorityFollowupSurvivesPrecedingNativeTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runtime *ClaudeRuntime
		var inputs []claude.Input
		native := claudeRequestFunc(func(_ context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
			var wire claude.Input
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			inputs = append(inputs, wire)
			queued, _ := json.Marshal(map[string]any{"type": "command_lifecycle", "state": "queued", "session_id": wire.SessionID, "uuid": "queued-" + wire.UUID, "command_uuid": wire.UUID})
			if event, err := runtime.HandleEvent(t.Context(), queued); err != nil || event.Started || event.Completed {
				t.Fatalf("queued lifecycle: %+v %v", event, err)
			}
			if len(inputs) == 1 {
				claudeStarted(t, runtime, wire)
			}
			return jsontext.Value(`{"status":"queued"}`), nil
		})
		runtime, _, _ = claudeFixture(t, native)
		turn, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "first", Kind: "NEW_TASK", Message: "first", Run: true})
		if err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			turn string
			err  error
		}
		done := make(chan outcome, 1)
		go func() {
			turn, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "queued", Kind: "NEW_TASK", Message: "queued", Run: true})
			done <- outcome{turn, err}
		}()
		synctest.Wait()
		if len(inputs) != 2 || inputs[1].Priority != "now" {
			t.Fatalf("priority followup not dispatched: %+v", inputs)
		}
		terminal, _ := json.Marshal(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": inputs[0].SessionID, "uuid": "first-terminal", "user_message_uuid": inputs[0].UUID, "user_message_uuids": []string{inputs[0].UUID}, "terminal_reason": "aborted_streaming"})
		if event, err := runtime.HandleEvent(t.Context(), terminal); err != nil || event.Completed || event.Answer != "" || runtime.CurrentTurn() != turn {
			t.Fatalf("preceding carrier settled admitted followup: %+v %v", event, err)
		}
		if runtime.Confirm(t.Context(), control.NativeInput{Thread: "child", Kind: "INTERRUPT", TargetTurn: turn}) == nil {
			t.Fatal("preceding carrier terminal confirmed interruption of current followup")
		}
		var first, queued claudeReceipt
		if err := runtime.read(claudeRecordName("child", "input", "first"), &first); err != nil || !first.Consumed || first.Input != nil || first.Terminal == nil || first.Terminal.Status != control.Interrupted || first.Terminal.UUID != "first-terminal" {
			t.Fatalf("preceding native outcome was lost: %+v %v", first, err)
		}
		if err := runtime.read(claudeRecordName("child", "input", "queued"), &queued); err != nil || queued.Accepted || queued.Consumed || queued.Input == nil {
			t.Fatalf("unnamed queued carrier was consumed/released: %+v %v", queued, err)
		}
		cancelled, _ := json.Marshal(map[string]any{"type": "command_lifecycle", "state": "cancelled", "session_id": inputs[0].SessionID, "uuid": "cancelled-first", "command_uuid": inputs[0].UUID})
		if event, err := runtime.HandleEvent(t.Context(), cancelled); err != nil || event.Completed {
			t.Fatalf("preceding cancellation settled followup: %+v %v", event, err)
		}
		if event := claudeStarted(t, runtime, inputs[1]); event.Started || event.Turn != turn {
			t.Fatalf("followup started competing application turn: %+v", event)
		}
		if result := <-done; result.err != nil || result.turn != turn {
			t.Fatalf("followup acknowledgement: %+v", result)
		}
		terminal, _ = json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": inputs[1].SessionID, "uuid": "followup-terminal", "user_message_uuid": inputs[1].UUID, "user_message_uuids": []string{inputs[1].UUID}, "terminal_reason": "completed", "result": "followup answer"})
		if event, err := runtime.HandleEvent(t.Context(), terminal); err != nil || !event.Completed || event.Status != control.Completed || event.Turn != turn || event.Answer != "followup answer" || runtime.CurrentTurn() != "" {
			t.Fatalf("current followup terminal was lost: %+v %v", event, err)
		}
	})
}

func TestClaudeInterruptCancelsQueuedInputWithoutReleasingMessages(t *testing.T) {
	for _, ordering := range []string{"native_event_first", "native_receipt_after_result", "worker_receipt_after_result"} {
		t.Run(ordering, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var runtime *ClaudeRuntime
				var inputs []claude.Input
				var published []ClaudeEvent
				observe := func(raw jsontext.Value) {
					event, err := runtime.HandleEvent(t.Context(), raw)
					if err != nil {
						t.Fatal(err)
					}
					if event.Completed {
						published = append(published, event)
						runtime.RetireCompleted(event.Turn, nil)
					}
				}
				native := claudeRequestFunc(func(_ context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
					if method == "interrupt" {
						if ordering == "native_event_first" {
							cancelled, _ := json.Marshal(map[string]any{"type": "command_lifecycle", "state": "cancelled", "session_id": inputs[1].SessionID, "uuid": "queued-cancelled", "command_uuid": inputs[1].UUID})
							observe(cancelled)
						}
						terminal, _ := json.Marshal(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": inputs[0].SessionID, "uuid": "active-interrupted", "user_message_uuid": inputs[0].UUID, "user_message_uuids": []string{inputs[0].UUID}, "terminal_reason": "aborted_streaming"})
						observe(terminal)
						receipt := map[string][]string{"still_queued": {}, "cancelled": {}, "worker_cancelled": {}}
						key := "cancelled"
						if ordering == "worker_receipt_after_result" {
							key = "worker_cancelled"
						}
						receipt[key] = []string{inputs[1].UUID}
						return json.Marshal(receipt)
					}
					var wire claude.Input
					if err := json.Unmarshal(raw, &wire); err != nil {
						t.Fatal(err)
					}
					inputs = append(inputs, wire)
					if len(inputs) != 2 {
						claudeStarted(t, runtime, wire)
					}
					return jsontext.Value(`{"status":"queued"}`), nil
				})
				runtime, _, _ = claudeFixture(t, native)
				runtime.SetEventSink(func(_ context.Context, event ClaudeEvent) error {
					if !runtime.mu.TryLock() {
						t.Fatal("interrupt lifecycle sink called under ledger lock")
					}
					runtime.mu.Unlock()
					if !runtime.dispatch.TryLock() {
						t.Fatal("interrupt lifecycle sink called under dispatch lock")
					}
					runtime.dispatch.Unlock()
					if err := runtime.Confirm(t.Context(), control.NativeInput{Thread: "child", Kind: "INTERRUPT", TargetTurn: event.Turn}); err != nil {
						t.Fatalf("sink preceded terminal retention: %v", err)
					}
					published = append(published, event)
					runtime.RetireCompleted(event.Turn, nil)
					return nil
				})
				turn, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "active", Kind: "NEW_TASK", Message: "active", Run: true})
				if err != nil {
					t.Fatal(err)
				}
				before := control.NativeInput{Thread: "child", ID: "before", Kind: "MESSAGE", Message: "message-before"}
				if _, err := runtime.Input(t.Context(), before); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					_, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "queued", Kind: "NEW_TASK", Message: "queued", Run: true})
					done <- err
				}()
				synctest.Wait()
				if _, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "after", Kind: "MESSAGE", Message: "message-after"}); err != nil {
					t.Fatal(err)
				}
				if err := runtime.Interrupt(t.Context(), "child", turn); err != nil {
					t.Fatal(err)
				}
				failure, ok := errors.AsType[*binding.DispatchError](<-done)
				if !ok || failure.Outcome != binding.HostRejected {
					t.Fatalf("cancelled queued input was accepted/left uncertain: %v", failure)
				}
				if len(published) != 1 || published[0].Status != control.Interrupted || published[0].Turn != turn || runtime.CurrentTurn() != "" || len(inputs) != 2 {
					t.Fatalf("interruption completion/dispatch mismatch: events=%+v inputs=%d", published, len(inputs))
				}
				late, _ := json.Marshal(map[string]any{"type": "command_lifecycle", "state": "cancelled", "session_id": inputs[1].SessionID, "uuid": "late-cancelled", "command_uuid": inputs[1].UUID})
				observe(late)
				if len(published) != 1 {
					t.Fatal("late native cancellation duplicated completion")
				}
				var cancelled, retained claudeReceipt
				if err := runtime.read(claudeRecordName("child", "input", "queued"), &cancelled); err != nil || !cancelled.Cancelled || cancelled.Consumed || cancelled.Input != nil || cancelled.Terminal == nil || cancelled.Terminal.Cancellation == "" {
					t.Fatalf("queued cancellation lost exact evidence: %+v %v", cancelled, err)
				}
				if err := runtime.read(claudeRecordName("child", "input", before.ID), &retained); err != nil || retained.Consumed || retained.Input == nil {
					t.Fatalf("queued messages lost while cancelling carrier: %+v %v", retained, err)
				}
				if next, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "explicit-next", Kind: "NEW_TASK", Message: "next", Run: true}); err != nil || next == turn || len(inputs) != 3 {
					t.Fatalf("explicit later work failed: %q %v", next, err)
				}
				body := string(inputs[2].Message)
				if before, after := strings.Index(body, "message-before"), strings.Index(body, "message-after"); before < 0 || after <= before {
					t.Fatalf("cancelled carrier reordered retained messages: %s", body)
				}
			})
		})
	}
}

func TestClaudeAdmittedInputRetiresAfterTerminalAcknowledgement(t *testing.T) {
	for _, boundary := range []string{"before_dispatch", "awaiting_started"} {
		t.Run(boundary, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var runtime *ClaudeRuntime
				var inputs []claude.Input
				native := claudeRequestFunc(func(_ context.Context, _ string, raw jsontext.Value) (jsontext.Value, error) {
					var input claude.Input
					if err := json.Unmarshal(raw, &input); err != nil {
						t.Fatal(err)
					}
					inputs = append(inputs, input)
					if len(inputs) == 1 {
						claudeStarted(t, runtime, input)
					}
					return jsontext.Value(`{"status":"queued"}`), nil
				})
				runtime, _, _ = claudeFixture(t, native)
				var completed []ClaudeEvent
				runtime.SetEventSink(func(_ context.Context, event ClaudeEvent) error {
					if err := runtime.Confirm(t.Context(), control.NativeInput{Thread: "child", Kind: "INTERRUPT", TargetTurn: event.Turn}); err != nil {
						t.Fatalf("logical completion preceded its terminal receipt: %v", err)
					}
					completed = append(completed, event)
					runtime.RetireCompleted(event.Turn, nil)
					return nil
				})
				turn, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "initial", Kind: "NEW_TASK", Message: "first", Run: true})
				if err != nil {
					t.Fatal(err)
				}
				release, err := runtime.AdmitControl(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				terminal := func(index int, answer string) ClaudeEvent {
					t.Helper()
					raw, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": inputs[index].SessionID, "uuid": "terminal-" + inputs[index].UUID, "user_message_uuid": inputs[index].UUID, "terminal_reason": "completed", "result": answer})
					event, err := runtime.HandleEvent(t.Context(), raw)
					if err != nil || event.Completed {
						t.Fatalf("terminal retired admitted dispatch: %+v %v", event, err)
					}
					return event
				}
				if boundary == "before_dispatch" {
					terminal(0, "preceding answer")
				}
				done := make(chan error, 1)
				go func() {
					next, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: "followup", Kind: "NEW_TASK", Message: "next", Run: true})
					if err == nil && next != turn {
						err = fmt.Errorf("followup changed admitted turn: %s", next)
					}
					done <- err
				}()
				synctest.Wait()
				if len(inputs) != 2 || inputs[1].Priority != "now" {
					t.Fatalf("admitted steering did not dispatch: %+v", inputs)
				}
				if boundary == "awaiting_started" {
					terminal(0, "preceding answer")
				}
				// A result can be the first exact acceptance evidence. The event
				// reader must release Input even while logical retirement waits.
				terminal(1, "followup answer")
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if runtime.CurrentTurn() != turn || len(completed) != 0 {
					t.Fatal("native completion retired work before input acknowledgement")
				}
				if err := release(); err != nil {
					t.Fatal(err)
				}
				if len(completed) != 1 || completed[0].Answer != "followup answer" || runtime.CurrentTurn() != "" {
					t.Fatalf("admission did not release exactly its final terminal: %+v", completed)
				}
			})
		})
	}
}
