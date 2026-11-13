package launch

import (
	"encoding/json/jsontext"
	"strconv"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestNativeFamilyProjectionSurvivesRootReplacement(t *testing.T) {
	old := &nativeFamily{threads: []binding.ThreadInfo{{ID: "old"}, {ID: "child", ParentThreadID: "old"}}}
	current := &nativeFamily{threads: []binding.ThreadInfo{{ID: "new"}}}
	c := &nativeConnection{families: map[string]*nativeFamily{"old": old, "new": current}}
	if root, _ := c.family("child"); root != "old" {
		t.Fatalf("root replacement reassigned child to %q", root)
	}
	if root, _ := c.family("new"); root != "new" {
		t.Fatalf("replacement acquired old family: %q", root)
	}
}

type nativeRoutingTransport struct {
	initializes int
	methods     []string
}

func (w *nativeRoutingTransport) Initialize() (string, error) { w.initializes++; return "0", nil }
func (w *nativeRoutingTransport) Send(method string, _ any, _ bool) (string, error) {
	w.methods = append(w.methods, method)
	return strconv.Itoa(len(w.methods)), nil
}
func (w *nativeRoutingTransport) Respond(jsontext.Value, any) error { return nil }

func TestNativeConnectionDemultiplexesOneLedgerAndThreadEvents(t *testing.T) {
	wire := new(nativeRoutingTransport)
	root := &nativeSession{Client: sessioncontrol.Client[appServerRequest]{Live: sessioncontrol.Live{Thread: "root"}, Requests: sessioncontrol.NewRequests[appServerRequest](wire)}}
	first := &nativeSession{Client: root.Client.ForThread("first")}
	second := &nativeSession{Client: root.Client.ForThread("second")}
	c := &nativeConnection{root: root, families: map[string]*nativeFamily{"root": {observed: true, threads: nil}}, byThread: make(map[string]*nativeEndpoint), buffered: make(map[string][]appserver.Message), seenRequests: make(map[string]string)}
	if _, err := root.Client.Requests.Initialize(appServerRequest{}); err != nil {
		t.Fatal(err)
	}
	root.Client.Requests.Take("0")
	seen := make(map[string][]appserver.Message)
	var ids []string
	for _, session := range []*nativeSession{root, first, second} {
		thread := session.Client.Thread
		session.dispatch = func(message appserver.Message) error {
			seen[thread] = append(seen[thread], message)
			if message.Method == "" {
				session.Client.Requests.Take(string(message.ID))
			}
			return nil
		}
		endpoint := &nativeEndpoint{session: session, before: func() error { return nil }, after: func() error { return nil }}
		c.add(endpoint)
		id, err := session.Client.Requests.Send("thread/read", map[string]any{"threadId": "different-metadata-target"}, "main", appServerRequest{metadata: "different-metadata-target"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, index := range []int{2, 0, 1} {
		if err := c.message(appserver.Message{ID: []byte(ids[index]), Result: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, thread := range []string{"second", "first", "root"} {
		if err := c.message(appserver.Message{Method: "turn/started", Params: []byte(`{"threadId":"` + thread + `"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	for index, thread := range []string{"root", "first", "second"} {
		if len(seen[thread]) != 2 || string(seen[thread][0].ID) != ids[index] || nativeMessageThread(seen[thread][1]) != thread {
			t.Fatalf("cross-thread routing for %s: %+v", thread, seen[thread])
		}
	}
	if first.Client.Host != nil || second.Client.Host != nil || wire.initializes != 1 {
		t.Fatal("child acquired a transport reader or initialized again")
	}
	if err := c.message(appserver.Message{Method: "item/started", Params: []byte(`{"threadId":"unknown-child"}`)}); err != nil {
		t.Fatal(err)
	}
	if len(seen["root"]) != 2 || wire.methods[len(wire.methods)-1] != "thread/subtree/list" || len(c.buffered["unknown-child"]) != 1 {
		t.Fatal("unknown child became root activity instead of passive discovery")
	}
}

func TestNativeConnectionRejectsEndWithoutNativeJoin(t *testing.T) {
	root := &nativeSession{Client: sessioncontrol.Client[appServerRequest]{Live: sessioncontrol.Live{Thread: "root"}}}
	child := &nativeSession{Client: sessioncontrol.Client[appServerRequest]{Live: sessioncontrol.Live{Thread: "child"}}}
	endpoint := &nativeEndpoint{session: child, ending: true}
	c := &nativeConnection{root: root, byThread: map[string]*nativeEndpoint{"child": endpoint}}
	request := &nativeEndRequest{thread: "child", reply: make(chan error, 1)}
	c.ended(request, appserver.Message{Error: &appserver.Error{Code: -32601, Message: "native endpoint unavailable"}})
	if <-request.reply == nil || endpoint.ending || c.byThread["child"] != endpoint {
		t.Fatal("missing native end was reported successful or retired live child")
	}
}

func TestNativeChildEventsWaitForHydratedIdentity(t *testing.T) {
	root := &nativeSession{Client: sessioncontrol.Client[appServerRequest]{Live: sessioncontrol.Live{Thread: "root"}}}
	child := &nativeSession{nativeAgent: &binding.ThreadInfo{ID: "child"}}
	endpoint := &nativeEndpoint{session: child}
	c := &nativeConnection{root: root, byThread: map[string]*nativeEndpoint{"child": endpoint}, buffered: make(map[string][]appserver.Message)}
	message := appserver.Message{ID: []byte(`"prompt"`), Method: "item/tool/requestUserInput", Params: []byte(`{"threadId":"child"}`)}
	if err := c.message(message); err != nil {
		t.Fatal(err)
	}
	if c.bufferedCount != 1 || len(c.buffered["child"]) != 1 || len(c.seenRequests) != 0 {
		t.Fatal("unhydrated child consumed or acknowledged its prompt")
	}
}

func TestNativeSideLifecycleInvalidatesItsOwnFamily(t *testing.T) {
	wire := new(nativeRoutingTransport)
	root := &nativeSession{Client: sessioncontrol.Client[appServerRequest]{Live: sessioncontrol.Live{Thread: "root"}, Requests: sessioncontrol.NewRequests[appServerRequest](wire)}}
	root.Client.Requests.TrackSide("side", &sessioncontrol.Side{Thread: "side", SideAnswer: &conversationmodel.SideAnswer{OneOff: true}})
	root.dispatch = func(appserver.Message) error { return nil }
	c := &nativeConnection{root: root, boundRoot: "root", families: map[string]*nativeFamily{
		"root": {observed: true, threads: nil},
		"side": {observed: true, threads: nil},
	}, byThread: make(map[string]*nativeEndpoint)}
	c.add(&nativeEndpoint{session: root, before: func() error { return nil }, after: func() error { return nil }})
	if err := c.message(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"side","turn":{"id":"side-turn","status":"completed"}}`)}); err != nil {
		t.Fatal(err)
	}
	if !c.families["side"].pending || c.families["root"].pending || c.families["root"].dirty || len(wire.methods) != 1 || wire.methods[0] != "thread/subtree/list" {
		t.Fatalf("side lifecycle invalidated another native root: %#v, %#v", c.families, wire.methods)
	}
}
