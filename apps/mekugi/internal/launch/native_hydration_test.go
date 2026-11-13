package launch

import (
	"context"
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestNativeHydrationBlockedStorageLeavesSiblingDispatchAvailable(t *testing.T) {
	service, err := history.NewService(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := service.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := newAppServerTestUI()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	u.ctx, u.history, u.retained.Store = ctx, service, store
	u.Client.Thread, u.Client.Startup.Thread = "", "child"
	u.nativeAgent = &appserver.AgentObservation{ThreadID: "child", ParentThreadID: new("root")}
	u.hydrationResults = make(chan *nativeHydration)
	c := &nativeConnection{root: &nativeSession{}, byThread: make(map[string]*nativeEndpoint)}
	u.connection = c
	entered, release, unlocked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(unlocked)
		if err := store.Transaction(t.Context(), nil, func(*history.Transaction) error { close(entered); <-release; return nil }); err != nil {
			t.Error(err)
		}
	}()
	<-entered
	t.Cleanup(func() { cancel(); close(release); <-unlocked; c.workers.Wait() })
	id, err := u.Client.Requests.Send("thread/resume", nil, "main", appServerRequest{})
	if err != nil {
		t.Fatal(err)
	}
	message := appserver.Message{ID: []byte(id), Result: []byte(`{"thread":{"id":"child","cwd":"/workspace"}}`)}
	if held, err := u.beginNativeHydration(message); !held || err != nil {
		t.Fatal(held, err)
	}
	if !u.hydrating || u.restoring == nil || !strings.Contains(u.attached.presentation.Status.Text, "Loading") {
		t.Fatal("blocked acquisition did not publish loading")
	}
	received := false
	sibling := &nativeSession{}
	sibling.Client.Thread = "sibling"
	sibling.dispatch = func(appserver.Message) error { received = true; return nil }
	endpoint := &nativeEndpoint{session: sibling, before: func() error { return nil }, after: func() error { return nil }}
	c.byThread["sibling"] = endpoint
	if err := c.handle(endpoint, func() error { return sibling.dispatch(appserver.Message{}) }); err != nil || !received {
		t.Fatal("child storage blocked sibling dispatch", err)
	}
	select {
	case <-u.hydrationResults:
		t.Fatal("storage lock was bypassed")
	default:
	}
	cancel()
	// Cancellation settles the blocked storage acquisition without needing the
	// shared event loop to receive or install a stale endpoint result.
	c.workers.Wait()
	if u.Client.Thread != "" || u.hydration != nil {
		t.Fatal("worker mutated endpoint identity")
	}
}

func TestNativeHydrationFailureDoesNotInstallOrReplay(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.hydrating = true
	called := false
	u.dispatch = func(appserver.Message) error { called = true; return nil }
	if err := u.acceptNativeHydration(&nativeHydration{err: context.Canceled}); err == nil {
		t.Fatal("failed acquisition reported success")
	}
	if called || u.hydration != nil || u.hydrating {
		t.Fatal("failed acquisition installed state or replayed response")
	}
}

func TestNativeHydrationOverflowDisclosesAndPreservesSiblingPrefix(t *testing.T) {
	root, _ := newAppServerTestUI()
	c := &nativeConnection{root: &root.nativeSession, buffered: make(map[string][]appserver.Message)}
	c.bufferHydration("sibling", appserver.Message{Method: "sibling-event"}, nil)
	for range 65 {
		c.bufferHydration("child", appserver.Message{Method: "child-event"}, nil)
	}
	if !c.suppressed["child"] || len(c.buffered["child"]) != 0 || len(c.buffered["sibling"]) != 1 || c.bufferedCount != 1 {
		t.Fatal("overflow discarded sibling state or retained incomplete child prefix")
	}
	if !root.attached.feedback.Alert || !strings.Contains(root.attached.feedback.Text, "select the agent again") {
		t.Fatal("overflow lacked recovery guidance")
	}
}

func TestNativeLogicalInterruptDoesNotClaimTerminalOutcome(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Client.NativeRoot = "root"
	u.Client.NativeWork = "work"
	u.Client.Waiting = "wait"
	intent := u.Client.Interrupt()
	if err := applyInterruption(&u.attached, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, u.review, &u.pendingEditReceipts, intent); err != nil {
		t.Fatal(err)
	}
	var request struct {
		Method string `json:"method"`
		Params struct {
			Root   string `json:"rootThreadId"`
			Thread string `json:"threadId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(wire.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "agent/interrupt" || request.Params.Root != "root" || request.Params.Thread != "main" || u.Client.NativeWork != "work" || u.Client.Waiting != "wait" {
		t.Fatal("logical interruption lost identity or fabricated terminal state")
	}
}

func TestNativeHydrationTransfersReadySnapshotOnlyOnEventLoop(t *testing.T) {
	u, _ := newAppServerTestUI()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store, err := u.history.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u.ctx, u.retained.Store = ctx, store
	u.retained.Limits = history.Limits{Total: history.DefaultStorageBytes}
	u.Client.Thread, u.Client.Startup.Thread = "", "child"
	u.nativeAgent = &appserver.AgentObservation{ThreadID: "child", ParentThreadID: new("root")}
	u.hydrationResults = make(chan *nativeHydration)
	u.connection = &nativeConnection{}
	t.Cleanup(func() { cancel(); u.connection.workers.Wait() })
	id, err := u.Client.Requests.Send("thread/resume", nil, "main", appServerRequest{})
	if err != nil {
		t.Fatal(err)
	}
	message := appserver.Message{ID: []byte(id), Result: []byte(`{"thread":{"id":"child","cwd":"/workspace"}}`)}
	held, err := u.beginNativeHydration(message)
	if !held || err != nil {
		t.Fatal(held, err)
	}
	result := <-u.hydrationResults
	if result.err != nil {
		t.Fatal(result.err)
	}
	if u.Client.Thread != "" || u.hydration != nil || !u.Client.Requests.Pending() {
		t.Fatal("worker installed state or consumed request correlation")
	}
	dispatched := false
	u.dispatch = func(actual appserver.Message) error {
		dispatched = true
		if string(actual.ID) != id || u.hydration != result || result.content.thread.ID != "child" || result.retained.Thread != "child" {
			t.Fatal("hydration transfer lost source, lease or response identity")
		}
		u.Client.Requests.Take(id)
		return nil
	}
	if err := u.acceptNativeHydration(result); err != nil {
		t.Fatal(err)
	}
	if !dispatched || u.hydrating || u.hydration != nil || u.Client.Requests.Pending() {
		t.Fatal("event-loop hydration did not settle once")
	}
}
