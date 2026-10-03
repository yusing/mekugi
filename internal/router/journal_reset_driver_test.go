package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

func resetDriverFixture(t *testing.T, mode string) (*journalResetDriver, *appServerTestInput) {
	t.Helper()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	proxy.journalCompaction = mode
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "plan", Reset: "slice", Tasks: []jsontext.Value{jsontext.Value(`{"title":"First","state":"working"}`), jsontext.Value(`"Second"`)}}}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, thread, "first-turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
		t.Fatal(err)
	}
	_, wire := newAppServerTestUI()
	client := &appserver.Client{Input: wire}
	d := &journalResetDriver{ctx: t.Context(), proxy: proxy, client: client, workspace: workspace, thread: thread, delay: time.Second}
	if err := d.completed("first-turn"); err != nil {
		t.Fatal(err)
	}
	if d.intent == nil || d.phase != "countdown" {
		t.Fatalf("completed slice did not start countdown: %+v", d)
	}
	d.deadline = time.Unix(101, 0)
	return d, wire
}

func resetDriverReply(t *testing.T, d *journalResetDriver, result string) {
	t.Helper()
	handled, err := d.message(appserver.Message{ID: jsontext.Value(d.requestID), Result: jsontext.Value(result)})
	if err != nil || !handled {
		t.Fatalf("RPC reply: handled=%v err=%v", handled, err)
	}
}
func resetDriverEvent(t *testing.T, d *journalResetDriver, method, params string) {
	t.Helper()
	handled, err := d.message(appserver.Message{Method: method, Params: jsontext.Value(params)})
	if err != nil || handled {
		t.Fatalf("lifecycle %s: handled=%v err=%v", method, handled, err)
	}
}
func resetDriverMethods(t *testing.T, wire *appServerTestInput) []string {
	t.Helper()
	var methods []string
	for _, line := range strings.Split(strings.TrimSpace(wire.String()), "\n") {
		if line == "" {
			continue
		}
		var m appserver.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, m.Method)
	}
	return methods
}
func resetDriverRequireMethods(t *testing.T, wire *appServerTestInput, want ...string) {
	t.Helper()
	got := resetDriverMethods(t, wire)
	if len(got) != len(want) {
		t.Fatalf("RPCs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RPCs = %v, want %v", got, want)
		}
	}
}

func TestJournalResetDriverCompactionRequiresOwnSuccessfulTurnAndAcknowledgement(t *testing.T) {
	t.Parallel()
	d, wire := resetDriverFixture(t, "slice")
	if d.phase != "countdown" {
		t.Fatalf("phase %q", d.phase)
	}
	if err := d.tick(time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "thread/compact/start")
	resetDriverEvent(t, d, "turn/started", `{"threadId":"other","turn":{"id":"compaction"}}`)
	resetDriverEvent(t, d, "item/completed", `{"threadId":"other","turnId":"compaction","item":{"type":"contextCompaction"}}`)
	resetDriverEvent(t, d, "turn/started", `{"threadId":"`+d.thread+`","turn":{"id":"compaction"}}`)
	resetDriverEvent(t, d, "item/completed", `{"threadId":"`+d.thread+`","turnId":"stale","item":{"type":"contextCompaction"}}`)
	resetDriverEvent(t, d, "turn/completed", `{"threadId":"other","turn":{"id":"compaction","status":"completed"}}`)
	resetDriverRequireMethods(t, wire, "thread/compact/start")
	// Router synthesis durably consumes the intent before Codex emits its item.
	if err := d.change(func(_ *threadJournal, intent *journalResetIntent) error { intent.Phase = "consumed"; return nil }); err != nil {
		t.Fatal(err)
	}
	resetDriverEvent(t, d, "item/completed", `{"threadId":"`+d.thread+`","turnId":"compaction","item":{"type":"contextCompaction"}}`)
	resetDriverEvent(t, d, "turn/completed", `{"threadId":"`+d.thread+`","turn":{"id":"compaction","status":"completed"}}`)
	resetDriverRequireMethods(t, wire, "thread/compact/start")
	resetDriverReply(t, d, `{}`)
	resetDriverRequireMethods(t, wire, "thread/compact/start", "turn/start")
	resetDriverReply(t, d, `{"turn":{"id":"continued"}}`)
	intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent != nil {
		t.Fatalf("intent after start: %+v %v", intent, err)
	}
}

func TestJournalResetDriverOffContinuesWithoutCompaction(t *testing.T) {
	t.Parallel()
	d, wire := resetDriverFixture(t, "off")
	if err := d.tick(time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "turn/start")
}

func TestJournalResetDriverRejectedCompactDoesNotRetry(t *testing.T) {
	t.Parallel()
	d, wire := resetDriverFixture(t, "slice")
	if err := d.tick(time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	handled, err := d.message(appserver.Message{ID: jsontext.Value(d.requestID), Error: &appserver.Error{Code: -1, Message: "rejected"}})
	if err != nil || !handled {
		t.Fatalf("rejection: %v %v", handled, err)
	}
	if err := d.tick(time.Unix(104, 0)); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "thread/compact/start")
}

func TestJournalResetDriverRestartOnlyReplaysPendingIntent(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"pending", "armed", "starting"} {
		t.Run(phase, func(t *testing.T) {
			d, wire := resetDriverFixture(t, "slice")
			if phase != "pending" {
				if err := d.change(func(_ *threadJournal, intent *journalResetIntent) error { intent.Phase = phase; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			restarted := &journalResetDriver{ctx: t.Context(), proxy: d.proxy, client: &appserver.Client{Input: wire}, workspace: d.workspace, thread: d.thread, delay: time.Second}
			wire.Reset()
			if err := restarted.restore(); err != nil {
				t.Fatal(err)
			}
			if phase == "pending" {
				if restarted.phase != "countdown" {
					t.Fatalf("restored phase %q", restarted.phase)
				}
				resetDriverRequireMethods(t, wire)
			} else {
				resetDriverRequireMethods(t, wire)
			}
		})
	}
}

func TestJournalResetDriverFailedCompactionNeverStartsContinuation(t *testing.T) {
	t.Parallel()
	d, wire := resetDriverFixture(t, "slice")
	if err := d.tick(time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	resetDriverReply(t, d, `{}`)
	resetDriverEvent(t, d, "turn/started", `{"threadId":"`+d.thread+`","turn":{"id":"compaction"}}`)
	resetDriverEvent(t, d, "turn/completed", `{"threadId":"`+d.thread+`","turn":{"id":"compaction","status":"failed"}}`)
	resetDriverRequireMethods(t, wire, "thread/compact/start")
	if err := d.tick(time.Unix(104, 0)); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "thread/compact/start")
}

func TestJournalResetDriverRejectedContinuationDoesNotRetry(t *testing.T) {
	t.Parallel()
	d, wire := resetDriverFixture(t, "off")
	if err := d.tick(time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	handled, err := d.message(appserver.Message{ID: jsontext.Value(d.requestID), Error: &appserver.Error{Code: -1, Message: "busy"}})
	if err != nil || !handled {
		t.Fatalf("rejection: %v %v", handled, err)
	}
	if err := d.tick(time.Unix(104, 0)); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "turn/start")
}

func TestJournalResetDriverCancelCountdownClearsIntent(t *testing.T) {
	t.Parallel()
	d, wire := resetDriverFixture(t, "slice")
	if err := d.cancel(); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire)
	intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent != nil {
		t.Fatalf("cancel intent: %+v %v", intent, err)
	}
}

func TestJournalResetDriverDoesNotReplaceCompactionTurn(t *testing.T) {
	t.Parallel()
	d, wire := resetDriverFixture(t, "slice")
	if err := d.tick(time.Unix(102, 0)); err != nil {
		t.Fatal(err)
	}
	resetDriverEvent(t, d, "turn/started", `{"threadId":"`+d.thread+`","turn":{"id":"first-turn"}}`)
	if d.compactTurn != "" {
		t.Fatal("old slice became compaction")
	}
	resetDriverEvent(t, d, "turn/started", `{"threadId":"`+d.thread+`","turn":{"id":"compact"}}`)
	resetDriverEvent(t, d, "turn/started", `{"threadId":"`+d.thread+`","turn":{"id":"other"}}`)
	if d.compactTurn != "compact" || d.active() {
		t.Fatalf("conflicting turn did not stop reset: %+v", d)
	}
	resetDriverRequireMethods(t, wire, "thread/compact/start")
}

func TestJournalResetDriverContinuationStartIdentity(t *testing.T) {
	t.Parallel()
	for _, beforeReply := range []bool{false, true} {
		t.Run(map[bool]string{false: "after reply", true: "before reply"}[beforeReply], func(t *testing.T) {
			d, _ := resetDriverFixture(t, "off")
			if err := d.tick(time.Unix(102, 0)); err != nil {
				t.Fatal(err)
			}
			event := func(id string) {
				resetDriverEvent(t, d, "turn/started", `{"threadId":"`+d.thread+`","turn":{"id":"`+id+`"}}`)
			}
			event("stale")
			if !d.startPending {
				t.Fatal("stale start cleared pending continuation")
			}
			if beforeReply {
				event("continued")
			}
			resetDriverReply(t, d, `{"turn":{"id":"continued"}}`)
			if !beforeReply {
				if !d.startPending {
					t.Fatal("ack lost pending start")
				}
				event("stale")
				if !d.startPending {
					t.Fatal("stale start cleared acknowledged continuation")
				}
				event("continued")
			}
			if d.active() {
				t.Fatalf("matching start left driver active: %+v", d)
			}
		})
	}
}

type resetFailingInput struct{}

func (resetFailingInput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (resetFailingInput) Close() error              { return nil }

func TestJournalResetDriverUncertainIntentDoesNotBlockLaterSlice(t *testing.T) {
	t.Parallel()
	d, wire := resetDriverFixture(t, "slice")
	d.client = &appserver.Client{Input: resetFailingInput{}}
	if err := d.tick(time.Unix(102, 0)); err == nil {
		t.Fatal("send failure was swallowed")
	}
	d.client = &appserver.Client{Input: wire}
	if d.active() {
		t.Fatal("uncertain dispatch left driver active")
	}
	if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Third"), State: new("pending")}}); err != nil {
		t.Fatal(err)
	}
	if err := d.proxy.journals.beginJournalTurn(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "second-turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{{Op: "set", P: "/2", State: new("done")}}); err != nil {
		t.Fatal(err)
	}
	if err := d.completed("second-turn"); err != nil {
		t.Fatal(err)
	}
	if d.intent == nil || d.intent.Path != "/3" || d.phase != "countdown" {
		t.Fatalf("later slice blocked by retained intent: intent=%+v phase=%q", d.intent, d.phase)
	}
	resetDriverRequireMethods(t, wire)
}

func TestJournalResetDriverRestoreDropsStaleIntents(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"armed", "later-turn", "slice-started"} {
		t.Run(scenario, func(t *testing.T) {
			d, wire := resetDriverFixture(t, "slice")
			switch scenario {
			case "armed":
				if err := d.change(func(_ *threadJournal, intent *journalResetIntent) error { intent.Phase = "armed"; return nil }); err != nil {
					t.Fatal(err)
				}
			case "later-turn":
				if err := d.proxy.journals.beginJournalTurn(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "later-turn"); err != nil {
					t.Fatal(err)
				}
			case "slice-started":
				if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{{Op: "set", P: "/2", State: new("working")}}); err != nil {
					t.Fatal(err)
				}
			}
			restarted := &journalResetDriver{ctx: t.Context(), proxy: d.proxy, client: &appserver.Client{Input: wire}, workspace: d.workspace, thread: d.thread, delay: time.Second}
			wire.Reset()
			if err := restarted.restore(); err != nil {
				t.Fatal(err)
			}
			resetDriverRequireMethods(t, wire)
			if (restarted.notice != "") != (scenario == "armed") {
				t.Fatalf("notice=%q", restarted.notice)
			}
			intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
			if err != nil || intent != nil {
				t.Fatalf("stale intent retained after restore: %+v %v", intent, err)
			}
		})
	}
}

func TestJournalResetDriverUnknownCompactDispatchDisarmsIntent(t *testing.T) {
	t.Parallel()
	d, _ := resetDriverFixture(t, "slice")
	d.client = &appserver.Client{Input: resetFailingInput{}}
	if err := d.tick(time.Unix(102, 0)); err == nil {
		t.Fatal("send failure was swallowed")
	}
	intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent == nil || intent.Phase != "unknown" {
		t.Fatalf("uncertain dispatch left intent consumable: %+v %v", intent, err)
	}
}

func TestJournalCompactionConsumesOnlyCurrentTurnResetIntent(t *testing.T) {
	t.Parallel()
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "later-turn"}[stale], func(t *testing.T) {
			d, _ := resetDriverFixture(t, "slice")
			if err := d.tick(time.Unix(102, 0)); err != nil {
				t.Fatal(err)
			}
			if stale {
				// The driver died after arming; the user kept working and later
				// compacted manually. That compaction is not the slice reset.
				if err := d.proxy.journals.beginJournalTurn(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "later-turn"); err != nil {
					t.Fatal(err)
				}
			}
			request, headers := journalCompactionRequest(t, d.workspace, d.thread)
			metadata, _ := decodeCodexTurnMetadata(headers)
			metadata.Compaction = mustTestJSON(t, map[string]any{
				"trigger": "manual", "reason": "user_requested", "implementation": "responses",
				"phase": "standalone_turn", "strategy": "memento",
			})
			headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
			wire, err := journalCompactionSSE("provider-compact", "gpt-test", "Provider recovery")
			if err != nil {
				t.Fatal(err)
			}
			response := serverHTTPResponse(string(wire))
			response.Header.Set("Content-Type", "text/event-stream")
			provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
			if err := executeRequest(t.Context(), t.Context(), request, headers, "compact", provider, io.Discard, nil, d.proxy); err != nil {
				t.Fatal(err)
			}
			intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
			if err != nil || intent == nil {
				t.Fatalf("intent: %+v %v", intent, err)
			}
			if stale && (len(provider.forwarded) != 1 || intent.Phase != "armed") {
				t.Fatalf("stale intent consumed: forwards=%d phase=%s", len(provider.forwarded), intent.Phase)
			}
			if !stale && (len(provider.forwarded) != 0 || intent.Phase != "consumed") {
				t.Fatalf("current reset not answered: forwards=%d phase=%s", len(provider.forwarded), intent.Phase)
			}
		})
	}
}
