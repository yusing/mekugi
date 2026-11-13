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

func TestProvisionalNativeAgentAdmitsNoDirectWork(t *testing.T) {
	live := Live{Thread: "child", NativeInputPending: true}
	if live.AcceptsInput() || live.AcceptsShell() || live.AcceptsSettings() {
		t.Fatal("provisional identity acquired direct work")
	}
	live.NativeInputPending = false
	if !live.AcceptsInput() || !live.AcceptsShell() || !live.AcceptsSettings() {
		t.Fatal("native initialization did not release direct controls")
	}
}
