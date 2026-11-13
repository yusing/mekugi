package sessioncontrol

import (
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestThreadEndpointsShareLedgerNotControlOrRetirement(t *testing.T) {
	wire := &requestTransport{id: "init"}
	root := Client[string]{Host: new(appserver.Client), Requests: NewRequests[string](wire), Live: Live{Thread: "root"}}
	if _, err := root.Requests.Initialize("startup"); err != nil {
		t.Fatal(err)
	}
	root.Requests.Take("init")
	first, second := root.ForThread("first"), root.ForThread("second")
	if first.Host != nil || second.Host != nil || first.Requests.requestLedger != root.Requests.requestLedger || second.Requests.requestLedger != root.Requests.requestLedger {
		t.Fatal("child acquired another connection or ledger")
	}
	for _, endpoint := range []*Client[string]{&root, &first, &second} {
		wire.id = endpoint.Thread
		// Scope and destination deliberately do not identify the consumer.
		if _, err := endpoint.Requests.Send("thread/read", map[string]any{"threadId": "metadata-target"}, "main", "view"); err != nil {
			t.Fatal(err)
		}
	}
	first.Requests.Attribute("first", "another-scope", "new-view")
	request, ok := root.Requests.Lookup("first")
	if !ok || request.Consumer != "first" || request.Owner != "new-view" {
		t.Fatalf("lost actual reply consumer: %+v", request)
	}
	root.Requests.Retire("main")
	if root.Requests.Pending() || !first.Requests.Pending() || !second.Requests.Pending() {
		t.Fatal("root retirement crossed endpoint boundary")
	}
	first.Requests.Close()
	first.Requests.RetireEndpoint()
	select {
	case <-second.Requests.closed:
		t.Fatal("child close stopped sibling")
	default:
	}
	if !second.Requests.Pending() {
		t.Fatal("child close retired sibling correlation")
	}
	if _, err := second.Requests.Initialize("forbidden"); err == nil || wire.initialized != 1 {
		t.Fatal("child initialized shared connection")
	}
	first.HookReview = true
	if first.Calls() != nil || second.Calls() == nil || root.Calls() == nil {
		t.Fatal("thread-local input gate blocked sibling")
	}
}

func TestNativeAgentMetricsPreserveUnavailableAndNativeCompactionScalar(t *testing.T) {
	unknown := NativeAgentDetails(appserver.AgentObservation{})
	if unknown.Model != nil || unknown.ContextTokens != nil || unknown.ContextWindow != nil || unknown.Compactions != nil {
		t.Fatalf("fabricated passive facts: %+v", unknown)
	}
	actual := NativeAgentDetails(appserver.AgentObservation{Model: new("child-model"), Metrics: appserver.AgentMetrics{ContextTokens: new(int64(0)), ContextWindow: new(int64(200000)), CompletedCompactions: new(uint64(17))}})
	if actual.ContextTokens == nil || *actual.ContextTokens != 0 || actual.Compactions == nil || *actual.Compactions != 17 || *actual.Model != "child-model" {
		t.Fatalf("lost native facts: %+v", actual)
	}
}

func TestNativeWaitingDoesNotNeedFabricatedTurnForQueueAdmission(t *testing.T) {
	live := Live{Thread: "child", Waiting: "native-wait"}
	if !live.Busy(false) || !live.PlanInput(QueueNextTurn, false).Queued || live.PlanInput(SendInput, false).Queued {
		t.Fatal("native wait lost queue versus human-enter distinction")
	}
	target := live.InterruptTarget()
	if target.Turn != "" || target.Stop != "" || target.Wait != "native-wait" {
		t.Fatalf("invented native host turn: %+v", target)
	}
}

func TestProvisionalNativeAgentAdmitsNoDirectWork(t *testing.T) {
	live := Live{Thread: "child", NativeInputPending: true}
	if live.AcceptsInput() || live.AcceptsShell() || live.AcceptsSettings() || live.NativeWork != "" {
		t.Fatal("provisional identity acquired direct work")
	}
	live.NativeInputPending = false
	if !live.AcceptsInput() || !live.AcceptsShell() || !live.AcceptsSettings() {
		t.Fatal("native initialization did not release direct controls")
	}
}
