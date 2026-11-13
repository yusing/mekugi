package launch

import (
	json "encoding/json/v2"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffrender"
)

// These are detached app-server observations, not a Go work/wait reducer.
func nativeRunningUI(t *testing.T) (*appServerUI, *threadExecution, *time.Time) {
	t.Helper()
	u, at := newActiveWorkTestUI(t)
	u.settings.Model, u.settings.Effort = "test-model", "high"
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	appServerTestTurn(t, u, "first")
	threads := &threadExecution{}
	c := newNativeConnection(&u.nativeSession, nil, threads, nil, nil, "", nil, false, false)
	threads.native, u.connection = c, c
	c.families = map[string]*nativeFamily{"main": {observed: true, tree: appserver.AgentTreeObservation{RootThreadID: "main", Agents: []appserver.AgentObservation{
		{ThreadID: "main", RootThreadID: "main", Path: new("/root"), Loaded: true, Attachable: new(true), CanAcceptDirectInput: new(true), Work: &appserver.AgentWorkObservation{WorkID: "main-work", Disposition: "running"}},
		{ThreadID: "child", RootThreadID: "main", ParentThreadID: new("main"), Path: new("/root/worker"), Attachable: new(true), CanAcceptDirectInput: new(true), Work: &appserver.AgentWorkObservation{WorkID: "child-work", Disposition: "running"}},
	}}}}
	c.add(&nativeEndpoint{session: &u.nativeSession, before: func() error { return nil }, after: func() error { return nil }})
	nativeTreeFixture(t, threads)
	return u, threads, at
}

func nativeTreeFixture(t *testing.T, threads *threadExecution) {
	t.Helper()
	c := threads.native
	tree := c.families["main"].tree
	tree.ObservationRevision++
	raw, err := json.Marshal(struct {
		Tree appserver.AgentTreeObservation `json:"tree"`
	}{tree})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.treeResponse("main", appserver.Message{Result: raw}); err != nil {
		t.Fatal(err)
	}
}

func nativeWaitUI(t *testing.T, timeout *int64) (*appServerUI, *threadExecution, *time.Time) {
	t.Helper()
	u, threads, at := nativeRunningUI(t)
	*at = at.Add(2 * time.Second)
	root := threads.native.agent("main")
	root.Work.Disposition = "waiting"
	root.Wait = &appserver.AgentWaitObservation{WaitID: "wait", Mode: "any", Phase: "registered", TimeoutMS: timeout, Selected: []appserver.AgentWaitTarget{{ThreadID: "child", WorkID: "child-work"}}}
	nativeTreeFixture(t, threads)
	nativeWaitItemFixture(t, u, "item/started", timeout)
	return u, threads, at
}

func nativeWaitItemFixture(t *testing.T, u *appServerUI, method string, timeout *int64) {
	t.Helper()
	status := "inProgress"
	if method == "item/completed" {
		status = "completed"
	}
	appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "first", "item": map[string]any{
		"id": "wait-call", "type": "collabAgentToolCall", "tool": "wait", "status": status, "senderThreadId": "main", "receiverThreadIds": []string{"child"}, "timeoutMs": timeout,
	}})
}

// nativeTrafficFixture uses the actual native raw-item carrier and its retained
// metadata, so live and restoration exercise the same presentation boundary.
func nativeTrafficFixture(t *testing.T, u *appServerUI, id, sender, recipient, kind, body, outcome string, at time.Time) map[string]any {
	t.Helper()
	item := map[string]any{"type": "agent_message", "id": id, "author": sender, "recipient": recipient,
		"content": []map[string]string{{"type": "input_text", "text": "Message Type: " + kind + "\nTask name: " + recipient + "\nSender: " + sender + "\nPayload:\n" + body}}}
	var facts *appserver.CoordinationProjection
	if outcome != "" {
		facts = &appserver.CoordinationProjection{Deliveries: []appserver.CoordinationTerminalDelivery{{DeliveryID: id, Source: appserver.CoordinationTarget{ThreadID: "child", TaskName: sender, WorkID: id + "-work"}, Disposition: outcome, Recipient: recipient, Body: body}}}
	}
	u.clock = func() time.Time { return at }
	appServerTestNotify(t, u, "rawResponseItem/completed", map[string]any{"threadId": "main", "item": item, "coordination": facts})
	return map[string]any{"timestamp": at.Format(time.RFC3339Nano), "type": "response_item", "payload": item, "metadata": map[string]any{"coordination": facts}}
}

func nativeWaitSettlementFixture(t *testing.T, u *appServerUI, result appserver.CoordinationWaitResult, outcomes []appserver.CoordinationWaitOutcome, messages []appserver.CoordinationWaitMessage) {
	t.Helper()
	mode := "any"
	if u.nativeAgent != nil && u.nativeAgent.Wait != nil {
		mode = u.nativeAgent.Wait.Mode
	}
	appServerTestNotify(t, u, "agent/coordination", appserver.AgentCoordination{ThreadID: "main", TurnID: "first", Projection: appserver.CoordinationProjection{Wait: &appserver.CoordinationWaitSettlement{WaitID: "wait", CallerThreadID: "main", CallerWorkID: "main-work", Origin: appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "first", ItemID: "exec"}, Mode: mode, Selected: []appserver.CoordinationTarget{{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}}, Result: result, Outcomes: outcomes, Messages: messages}}})
	if u.nativeAgent != nil && u.nativeAgent.Wait != nil {
		nativeWaitItemFixture(t, u, "item/completed", u.nativeAgent.Wait.TimeoutMS)
	}
}

func publishNativeDelivery(attached *attachedPresentation, input sessioncontrol.NativeDelivery, now time.Time) {
	entry := sessionpresentation.NativeDeliveryEntry(input, input.ID, now, false)
	entry.Seq = attached.session.Activity().NextSequence()
	sessionpresentation.ApplyActivity(attached.source, attached.childSource, []sessionpresentation.ActivityEntry{entry}, nil)
}
