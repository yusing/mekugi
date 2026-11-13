package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strconv"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestNativeCheckpointTaggedVariantsKeepLogicalWorkUnsettled(t *testing.T) {
	for _, checkpoint := range []string{
		`{"kind":"completion_pending","delivery_id":"final-delivery"}`,
	} {
		t.Run(checkpoint, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.Client.Turn = "native-turn"
			u.attached.session.Lifecycle("main").Final = true
			params := []byte(`{"threadId":"main","turnId":"native-turn","workId":"logical-work","checkpoint":` + checkpoint + `,"targets":["child"]}`)
			handled, err := u.observeNativeCheckpoint(appserver.Message{Method: "turn/checkpointed", Params: params})
			if !handled || err != nil {
				t.Fatal(handled, err)
			}
			if u.Client.Turn != "" || u.Client.NativeWork != "logical-work" || !u.Client.Busy(false) || u.attached.session.Lifecycle("main").Final || wire.Len() != 0 {
				t.Fatal("checkpoint completed work, invented a host turn or dispatched continuation")
			}
			var value appserver.TurnCheckpoint
			if err := json.Unmarshal([]byte(checkpoint), &value); err != nil {
				t.Fatal(err)
			}
			if u.Client.Waiting != "" || u.nativeCheckpoint.Checkpoint.DeliveryID != value.DeliveryID {
				t.Fatal("tagged checkpoint attribution was lost")
			}
		})
	}
}

func TestNativeFamilyProjectionSurvivesRootReplacement(t *testing.T) {
	old := &nativeFamily{tree: appserver.AgentTreeObservation{RootThreadID: "old", Agents: []appserver.AgentObservation{{ThreadID: "old", RootThreadID: "old"}, {ThreadID: "child", RootThreadID: "old", ParentThreadID: new("old")}}}}
	current := &nativeFamily{tree: appserver.AgentTreeObservation{RootThreadID: "new", Agents: []appserver.AgentObservation{{ThreadID: "new", RootThreadID: "new"}}}}
	c := &nativeConnection{families: map[string]*nativeFamily{"old": old, "new": current}}
	if root, _ := c.family("child"); root != "old" {
		t.Fatalf("root replacement reassigned child to %q", root)
	}
	if root, _ := c.family("new"); root != "new" {
		t.Fatalf("replacement acquired old family: %q", root)
	}
}

func TestNativeCompletedWorkSettlesCheckpointThroughTreeNotification(t *testing.T) {
	u, threads := nativeCompletionCheckpoint(t)
	c := threads.native
	if u.Client.Turn != "" || !sessionpresentation.MainInterruptible(&u.Client.Live, u.attached.conversation) {
		t.Fatal("completion checkpoint retained a host turn or lost work interruption")
	}
	// Late publication invalidates the native tree, without another actor turn
	// or a synthetic turn/completed notification.
	tree := c.families["main"].tree
	tree.ObservationRevision++
	tree.Agents = append([]appserver.AgentObservation(nil), tree.Agents...)
	for i := range tree.Agents {
		previous := tree.Agents[i].Work
		tree.Agents[i].Work = &appserver.AgentWorkObservation{WorkID: previous.WorkID, Revision: previous.Revision + 1, Disposition: "completed", TerminalDeliveryID: new("final-delivery")}
	}
	params, err := json.Marshal(appserver.AgentTreeChanged{RootThreadID: "main", ObservationRevision: tree.ObservationRevision})
	trafficCheck(t, err)
	trafficCheck(t, c.message(appserver.Message{Method: "agent/treeChanged", Params: params}))
	wire := u.Host.Input.(*appServerTestInput)
	request := appServerOneRequest(t, wire, "agent/observeTree", "")
	if !u.Busy(false) || u.attached.session.Lifecycle("main").Final {
		t.Fatal("invalidation alone settled native work")
	}
	result, err := json.Marshal(struct {
		Tree appserver.AgentTreeObservation `json:"tree"`
	}{tree})
	trafficCheck(t, err)
	id, err := json.Marshal(request.ID)
	trafficCheck(t, err)
	trafficCheck(t, c.message(appserver.Message{ID: id, Result: result}))
	u.testMainFrame(80, 8, 0)
	if u.Client.Turn != "" || u.Client.NativeWork != "" || u.Busy(false) || !u.attached.session.Lifecycle("main").Final || !u.attached.presentation.View().Status.Session.CompletionKnown {
		t.Fatal("late native completion left the originating view active")
	}
	if sessionpresentation.MainInterruptible(&u.Client.Live, u.attached.conversation) {
		t.Fatal("completed checkpoint remained interruptible")
	}
	if c.agent("main").Work.Disposition != "completed" || wire.Len() != 0 {
		t.Fatal("headless completion fact was lost or publication started inference")
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
	c := &nativeConnection{root: root, families: map[string]*nativeFamily{"root": {observed: true, tree: appserver.AgentTreeObservation{RootThreadID: "root"}}}, byThread: make(map[string]*nativeEndpoint), buffered: make(map[string][]appserver.Message), seenRequests: make(map[string]string)}
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
	if len(seen["root"]) != 2 || wire.methods[len(wire.methods)-1] != "agent/observeTree" || len(c.buffered["unknown-child"]) != 1 {
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
	child := &nativeSession{nativeAgent: &appserver.AgentObservation{ThreadID: "child"}}
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
