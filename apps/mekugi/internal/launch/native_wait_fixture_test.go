package launch

import (
	json "encoding/json/v2"
	"strconv"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"

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
	c.families = map[string]*nativeFamily{"main": {observed: true, threads: []binding.ThreadInfo{
		{ID: "main", Source: []byte(`{"subAgent":{"thread_spawn":{"agent_path":"/root"}}}`), CanAcceptDirectInput: new(true), AgentStatus: []byte(`"running"`)},
		{ID: "child", ParentThreadID: "main", Source: []byte(`{"subAgent":{"thread_spawn":{"agent_path":"/root/worker"}}}`), CanAcceptDirectInput: new(true), AgentStatus: []byte(`"running"`)},
	}}}
	c.suppressed = map[string]bool{"child": true}
	c.add(&nativeEndpoint{session: &u.nativeSession, before: func() error { return nil }, after: func() error { return nil }})
	nativeTreeFixture(t, threads)
	return u, threads, at
}

func nativeTreeFixture(t *testing.T, threads *threadExecution) {
	t.Helper()
	c := threads.native
	members := c.families["main"].threads
	responseID, _ := strconv.Atoi(c.families["main"].response)
	raw, err := json.Marshal(struct {
		Data []binding.ThreadInfo `json:"data"`
	}{members})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.treeResponse("main", appserver.Message{ID: []byte(strconv.Itoa(responseID + 1)), Result: raw}); err != nil {
		t.Fatal(err)
	}
}

func nativeWaitUI(t *testing.T, timeout *int64) (*appServerUI, *threadExecution, *time.Time) {
	t.Helper()
	u, threads, at := nativeRunningUI(t)
	*at = at.Add(2 * time.Second)
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

func nativeWaitSettlementFixture(t *testing.T, u *appServerUI, result appserver.CoordinationWaitResult, outcomes []appserver.CoordinationWaitOutcome, messages []appserver.CoordinationWaitMessage, modes ...string) {
	t.Helper()
	mode := "any"
	if len(modes) != 0 {
		mode = modes[0]
	}
	appServerTestNotify(t, u, "agent/coordination", appserver.AgentCoordination{ThreadID: "main", TurnID: "first", Projection: appserver.CoordinationProjection{Wait: &appserver.CoordinationWaitSettlement{WaitID: "wait", CallerThreadID: "main", CallerWorkID: "main-work", Origin: appserver.CoordinationWaitOrigin{Kind: "cell", TurnID: "first", ItemID: "exec"}, Mode: mode, Selected: []appserver.CoordinationTarget{{ThreadID: "child", TaskName: "/root/worker", WorkID: "child-work"}}, Result: result, Outcomes: outcomes, Messages: messages}}})
	for _, entry := range u.attached.source.Entries {
		if entry.Source.CallID == "wait-call" {
			nativeWaitItemFixture(t, u, "item/completed", nil)
			break
		}
	}
}

func publishNativeDelivery(attached *attachedPresentation, input sessioncontrol.NativeDelivery, now time.Time) {
	entry := sessionpresentation.NativeDeliveryEntry(input, input.ID, now, false)
	entry.Seq = attached.session.Activity().NextSequence()
	sessionpresentation.ApplyActivity(attached.source, attached.childSource, []sessionpresentation.ActivityEntry{entry}, nil)
}
