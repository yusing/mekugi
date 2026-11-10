package sessionapi

import (
	"context"
	json "encoding/json/v2"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

type frontendFixture struct {
	hub      *Hub
	server   *httptest.Server
	api      *Server
	requests chan NativeRequest
	frames   chan NativeFrame
}

func newFrontendFixture(t *testing.T) *frontendFixture {
	t.Helper()
	api := NewLive()
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", api, Summary{Thread: "thread", Workspace: "/workspace"})
	if err := api.Publish(Frame{Session: Session{Thread: "thread", Workspace: "/workspace"}, Attachment: 1}); err != nil {
		t.Fatal(err)
	}
	fixture := &frontendFixture{hub: hub, api: api, requests: make(chan NativeRequest, 64), frames: make(chan NativeFrame)}
	fixture.server = httptest.NewServer(Versioned(hub.Handler()))
	t.Cleanup(fixture.server.Close)
	go func() {
		frame := nativeTestFrame("thread", 1)
		frame.View.Displayed = frame.View.Main
		frame.View.Info.Caller = "/root"
		attachments := map[string]bool{}
		for {
			select {
			case <-t.Context().Done():
				return
			case next := <-fixture.frames:
				frame = next
				for attachment := range attachments {
					_ = api.Native().Publish(attachment, frame)
				}
			case request := <-api.Native().Requests():
				if request.Attach {
					attachments[request.Attachment] = true
				}
				_ = api.Native().Publish(request.Attachment, frame)
				request.Reply <- ""
				fixture.requests <- request
			}
		}
	}()
	return fixture
}

func (f *frontendFixture) open(t *testing.T, pid int) *NativeConnection {
	t.Helper()
	connection, err := OpenNative(t.Context(), f.server.Client(), f.server.URL, "thread")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	if _, err = connection.Next(); err != nil {
		t.Fatal(err)
	}
	<-f.requests
	if err = connection.Activate(t.Context(), pid, 1); err != nil {
		t.Fatal(err)
	}
	return connection
}

func (f *frontendFixture) call(t *testing.T, method string, request NativeFrontendRequest) (NativeObservation, *RPCError) {
	t.Helper()
	rpc, err := OpenRPC(t.Context(), f.server.Client(), f.server.URL+NativeRPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	body, _ := json.Marshal(request)
	var result NativeObservation
	err = rpc.Call(t.Context(), method, RPCParams{Body: body}, &result)
	if err != nil {
		failure, ok := err.(*RPCError)
		if !ok {
			t.Fatal(err)
		}
		return result, failure
	}
	return result, nil
}

func TestNativeFrontendIsolationActivationAndLeases(t *testing.T) {
	f := newFrontendFixture(t)
	first := f.open(t, 101)
	second := f.open(t, 202)
	observed, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 101, Viewer: "panel"})
	if failure != nil || !observed.Available || observed.Displayed.ID != "thread" {
		t.Fatalf("observation: %+v %v", observed, failure)
	}
	lease := <-f.requests
	if lease.Operation.Viewer == nil || lease.Attachment != first.last.Attachment || lease.Operation.Viewer.Expires <= time.Now().UnixMilli() {
		t.Fatalf("lease not on original attachment: %+v", lease)
	}
	other, failure := f.call(t, "native/resolve", NativeFrontendRequest{PID: 202, Instance: "stale"})
	if failure == nil || other.Available {
		t.Fatal("stale instance admitted")
	}
	f.hub.mu.Lock()
	var otherOwner *rpcConnection
	for owner := range f.hub.connections {
		if owner.frontend.pid == 202 {
			otherOwner = owner
		}
	}
	isolated := otherOwner != nil && len(otherOwner.frontend.viewers) == 0
	otherInstance := ""
	if otherOwner != nil {
		otherInstance = otherOwner.frontend.instance
	}
	f.hub.mu.Unlock()
	if !isolated {
		t.Fatal("lease leaked to second physical frontend")
	}
	if otherInstance == observed.Instance {
		t.Fatal("physical frontend identity shared")
	}
	if failed, err := first.Attach(t.Context(), "missing"); err == nil {
		failed.Close()
		t.Fatal("unknown destination attached")
	}
	// Merely admitting another native subscription cannot change the association.
	next, err := first.Attach(t.Context(), "thread")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	<-f.requests
	before, failure := f.call(t, "native/resolve", NativeFrontendRequest{PID: 101, Instance: observed.Instance})
	if failure != nil || before.Attachment != observed.Attachment {
		t.Fatal("unaccepted handoff became active")
	}
	if _, err = next.Next(); err != nil {
		t.Fatal(err)
	}
	if err = next.Activate(t.Context(), 101, 3); err != nil {
		t.Fatal(err)
	}
	transferred := <-f.requests
	if transferred.Operation.Viewer == nil || transferred.Operation.Viewer.Revision != lease.Operation.Viewer.Revision || transferred.Operation.Viewer.Expires != lease.Operation.Viewer.Expires {
		t.Fatal("handoff restarted or changed lease")
	}
	// Concurrent handler scheduling cannot apply an earlier activation later.
	if err = first.Activate(t.Context(), 101, 2); err != nil {
		t.Fatal(err)
	}
	current, failure := f.call(t, "native/resolve", NativeFrontendRequest{PID: 101, Instance: observed.Instance})
	if failure != nil || current.Attachment == observed.Attachment {
		t.Fatal("older activation retargeted frontend")
	}
	if _, failure = f.call(t, "native/select", NativeFrontendRequest{PID: 101, Instance: observed.Instance, Attachment: observed.Attachment, Displayed: "thread", Agent: "/root"}); failure == nil {
		t.Fatal("stale attachment navigated")
	}
	if _, failure = f.call(t, "native/select", NativeFrontendRequest{PID: 101, Instance: observed.Instance, Activation: current.Activation, Attachment: current.Attachment, Displayed: "thread", Agent: "/root"}); failure != nil {
		t.Fatal(failure)
	}
	selected := <-f.requests
	if selected.Operation.Navigate == nil || selected.Operation.Navigate.Activation != current.Activation {
		t.Fatal("navigation lost captured activation")
	}
	if err = first.Activate(t.Context(), 101, 4); err != nil {
		t.Fatal(err)
	}
	<-f.requests
	returned, failure := f.call(t, "native/resolve", NativeFrontendRequest{PID: 101, Instance: observed.Instance})
	if failure != nil || returned.Attachment != observed.Attachment || returned.Activation != 4 {
		t.Fatal("return activation did not keep original view")
	}
	if _, failure = f.call(t, "native/select", NativeFrontendRequest{PID: 101, Instance: observed.Instance, Activation: observed.Activation, Attachment: observed.Attachment, Displayed: "thread", Agent: "/root"}); failure == nil {
		t.Fatal("stale A→B→A selection admitted")
	}
	current = returned
	if _, failure = f.call(t, "native/release", NativeFrontendRequest{PID: 101, Instance: observed.Instance, Attachment: current.Attachment, Viewer: "panel"}); failure != nil {
		t.Fatal(failure)
	}
	released := <-f.requests
	if released.Operation.Viewer == nil || released.Operation.Viewer.Revision <= lease.Operation.Viewer.Revision || released.Operation.Viewer.Expires != 0 {
		t.Fatal("release did not restore terminal")
	}
	// Same PID on a new physical connection retires the former instance.
	if err = second.Activate(t.Context(), 101, 2); err == nil {
		t.Fatal("physical connection changed its process")
	}
	replacement := f.open(t, 101)
	_ = replacement
	if _, failure = f.call(t, "native/resolve", NativeFrontendRequest{PID: 101, Instance: observed.Instance}); failure == nil {
		t.Fatal("PID reuse accepted stale instance")
	}
}

func TestNativeFrontendHeaderIsPhysicalOrderedAndDetached(t *testing.T) {
	f := newFrontendFixture(t)
	first, second := f.open(t, 101), f.open(t, 202)
	header := sessionpresentation.FrontendHeader{
		Displayed: sessionpresentation.Thread{ID: "thread", Caller: "/root"},
		Name:      "Main", Title: "Keep the session information", Skills: []string{"commit", "plane"}, Above: 703,
	}
	if err := first.PublishHeader(t.Context(), 101, 1, 1, header); err != nil {
		t.Fatal(err)
	}
	observed, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 101, Viewer: "header"})
	if failure != nil || observed.Header == nil || !observed.Header.Equal(header) {
		t.Fatalf("header publication: %+v %v", observed.Header, failure)
	}
	f.api.mu.Lock()
	encoded := f.api.nativePanel
	f.api.mu.Unlock()
	header.Above = 700
	if err := first.PublishHeader(t.Context(), 101, 1, 2, header); err != nil {
		t.Fatal(err)
	}
	latest, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 101, Viewer: "header"})
	if failure != nil || latest.Header == nil || !latest.Header.Equal(header) || observed.Cursor == latest.Cursor {
		t.Fatalf("physical scroll did not change observation: %+v %v", latest.Header, failure)
	}
	f.api.mu.Lock()
	reused := encoded == f.api.nativePanel
	f.api.mu.Unlock()
	if !reused {
		t.Fatal("physical scroll recomposed the shared panel payload")
	}
	stale := header
	stale.Title = "stale"
	if err := first.PublishHeader(t.Context(), 101, 1, 1, stale); err != nil {
		t.Fatal(err)
	}
	other, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 202, Viewer: "other"})
	if failure != nil || other.Header != nil {
		t.Fatal("physical header leaked to another terminal")
	}
	if err := second.PublishHeader(t.Context(), 101, 1, 3, stale); err == nil {
		t.Fatal("another association replaced the header")
	}
	invalid := header
	invalid.Above = -1
	if err := first.PublishHeader(t.Context(), 101, 1, 3, invalid); err == nil {
		t.Fatal("negative scroll admitted")
	}
	latest, failure = f.call(t, "native/observe", NativeFrontendRequest{PID: 101, Viewer: "header"})
	if failure != nil || latest.Header == nil || !latest.Header.Equal(header) {
		t.Fatal("stale or rejected publication changed the header")
	}
	if err := first.Activate(t.Context(), 101, 2); err != nil {
		t.Fatal(err)
	}
	if err := first.PublishHeader(t.Context(), 101, 1, 99, stale); err != nil {
		t.Fatal(err)
	}
	latest, failure = f.call(t, "native/observe", NativeFrontendRequest{PID: 101, Viewer: "header"})
	if failure != nil || latest.Header != nil {
		t.Fatal("new activation inherited the previous physical header")
	}
	wrong := header
	wrong.Displayed.Caller = "/root/other"
	if err := first.PublishHeader(t.Context(), 101, 2, 1, wrong); err != nil {
		t.Fatal(err)
	}
	latest, failure = f.call(t, "native/observe", NativeFrontendRequest{PID: 101, Viewer: "header"})
	if failure != nil || latest.Header != nil {
		t.Fatal("header for different displayed content was exposed")
	}
}

func TestNativeFrontendObserveDisplayChangeAndHandoff(t *testing.T) {
	f := newFrontendFixture(t)
	connection := f.open(t, 303)
	initial, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 303, Viewer: "panel"})
	if failure != nil {
		t.Fatal(failure)
	}
	<-f.requests
	for _, handoff := range []bool{false, true} {
		done := make(chan NativeObservation, 1)
		errors := make(chan *RPCError, 1)
		go func() {
			result, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 303, Instance: initial.Instance, Viewer: "panel", After: initial.Cursor, Attachment: initial.Attachment})
			errors <- failure
			done <- result
		}()
		<-f.requests // Lease renewal proves the observer was admitted.
		if handoff {
			next, err := connection.Attach(t.Context(), "thread")
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			<-f.requests
			if _, err = next.Next(); err != nil {
				t.Fatal(err)
			}
			if err = next.Activate(t.Context(), 303, 2); err != nil {
				t.Fatal(err)
			}
			<-f.requests
		} else {
			frame := nativeTestFrame("thread", 1)
			frame.View.Displayed = sessionpresentation.Thread{ID: "historical", Caller: "/root/old"}
			frame.View.Info.Caller = "/root"
			frame.View.Version.Revision++
			f.frames <- frame
		}
		select {
		case result := <-done:
			if failure := <-errors; failure != nil {
				t.Fatal(failure)
			}
			if result.Cursor == initial.Cursor || !handoff && result.Displayed.ID != "historical" || handoff && result.Attachment == initial.Attachment {
				t.Fatalf("observer missed native change: %+v", result)
			}
			initial = result
		case <-time.After(3 * time.Second):
			t.Fatal("observation waited only on old shared runtime")
		}
	}
}

func TestNativeViewerLeaseExpiryAndOrdering(t *testing.T) {
	now := time.Unix(1000, 0)
	frontend := nativeFrontend{viewers: map[string]nativeViewer{"first": {expires: now.Add(time.Second), fileOpen: true}, "second": {expires: now.Add(2 * time.Second)}}, revision: 3}
	if lease := frontend.lease(now); lease.Revision != 3 || lease.Expires != now.Add(2*time.Second).UnixMilli() {
		t.Fatal("effective lease did not retain latest viewer")
	}
	if lease := frontend.lease(now); lease.FileOpenExpires != now.Add(time.Second).UnixMilli() {
		t.Fatal("file-opening capability did not retain its own deadline")
	}
	if lease := frontend.lease(now.Add(time.Second)); lease.Expires != now.Add(2*time.Second).UnixMilli() || len(frontend.viewers) != 1 {
		t.Fatal("expiry released another viewer")
	}
	if lease := frontend.lease(now.Add(time.Second)); lease.FileOpenExpires != 0 {
		t.Fatal("ordinary viewer extended file-opening capability")
	}
	if lease := frontend.lease(now.Add(2 * time.Second)); lease.Expires != 0 || len(frontend.viewers) != 0 {
		t.Fatal("expired viewer retained")
	}
}

func TestNativeFrontendLeaseDeliveryFollowsCompletedHandoff(t *testing.T) {
	for _, test := range []struct {
		name, method string
		handoff      bool
	}{
		{"observe", "native/observe", true},
		{"release", "native/release", true},
		{"current_refusal", "native/observe", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := NewHub(t.Context(), nil)
			defer hub.Close()
			waiting := make(chan NativeViewerLease, 1)
			delivered := make(chan NativeViewerLease, 4)
			for _, thread := range []string{"first", "second"} {
				api := NewLive()
				registerTestSession(hub, thread, api, Summary{Thread: thread, Workspace: "/workspace"})
				if err := api.Publish(Frame{Session: Session{Thread: thread, Workspace: "/workspace"}, Attachment: 1}); err != nil {
					t.Fatal(err)
				}
				go func() {
					frame := nativeTestFrame(thread, 1)
					frame.View.Displayed = frame.View.Main
					viewers := 0
					for {
						select {
						case <-t.Context().Done():
							return
						case request := <-api.Native().Requests():
							if request.Operation.Viewer != nil {
								viewers++
								if thread == "first" && viewers == 2 {
									if !test.handoff {
										request.Reply <- "pending presentation consequences"
										continue
									}
									waiting <- *request.Operation.Viewer
									<-request.Context.Done()
									continue
								}
								if thread == "second" {
									delivered <- *request.Operation.Viewer
								}
							}
							_ = api.Native().Publish(request.Attachment, frame)
							request.Reply <- ""
						}
					}
				}()
			}
			server := httptest.NewServer(Versioned(hub.Handler()))
			defer server.Close()
			first, err := OpenNative(t.Context(), server.Client(), server.URL, "first")
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			if _, err = first.Next(); err != nil {
				t.Fatal(err)
			}
			if err = first.Activate(t.Context(), 912, 1); err != nil {
				t.Fatal(err)
			}
			rpc, err := OpenRPC(t.Context(), server.Client(), server.URL+NativeRPCPath, "")
			if err != nil {
				t.Fatal(err)
			}
			defer rpc.Close()
			var initial NativeObservation
			if err = rpc.Call(t.Context(), "native/observe", RPCParams{Body: mustJSON(t, NativeFrontendRequest{PID: 912, Viewer: "panel"})}, &initial); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				value NativeObservation
				err   error
			}
			pending := make(chan outcome, 1)
			go func() {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				var value NativeObservation
				err := rpc.Call(ctx, test.method, RPCParams{Body: mustJSON(t, NativeFrontendRequest{PID: 912, Instance: initial.Instance, Viewer: "panel", Attachment: initial.Attachment, After: initial.Cursor})}, &value)
				pending <- outcome{value, err}
			}()
			if !test.handoff {
				result := <-pending
				failure, ok := result.err.(*RPCError)
				if !ok || failure.Message != "pending presentation consequences" || failure.Data == nil || failure.Data.Outcome != "not_admitted" {
					t.Fatalf("genuine current-owner failure was swallowed: %+v", result)
				}
				return
			}
			var original NativeViewerLease
			select {
			case original = <-waiting:
			case <-time.After(time.Second):
				t.Fatal("old attachment did not receive delayed lease")
			}
			second, err := first.Attach(t.Context(), "second")
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			if _, err = second.Next(); err != nil {
				t.Fatal(err)
			}
			if err = second.Activate(t.Context(), 912, 2); err != nil {
				t.Fatal(err)
			}
			first.Close()
			result := <-pending
			if result.err != nil || !result.value.Available {
				t.Fatalf("retiring attachment broke valid frontend observation: %+v", result)
			}
			if test.method == "native/observe" && (result.value.Thread != "second" || result.value.Attachment != second.last.Attachment) {
				t.Fatalf("observer did not follow completed destination: %+v", result.value)
			}
			for range 2 {
				select {
				case lease := <-delivered:
					if lease != original {
						t.Fatalf("handoff repeated renewal or changed ordering: original=%+v delivered=%+v", original, lease)
					}
				case <-time.After(time.Second):
					t.Fatal("lease snapshot was not delivered to the current attachment")
				}
			}
		})
	}
}

func TestNativeFrontendCallerFollowsDisplayedContent(t *testing.T) {
	for _, test := range []struct {
		name                string
		displayed           sessionpresentation.Thread
		runtimeCaller, want string
	}{
		{"historical", sessionpresentation.Thread{ID: "historical", Caller: "/root/old"}, "/root", "/root/old"},
		{"fallback", sessionpresentation.Thread{ID: "historical"}, "/root", "/root"},
		{"main_canonical", sessionpresentation.Thread{ID: "thread", Caller: "/root"}, "/root/child", "/root/child"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFrontendFixture(t)
			connection := fixture.open(t, 911)
			frame := nativeTestFrame("thread", 1)
			frame.View.Info.Caller = test.runtimeCaller
			frame.View.Displayed = test.displayed
			frame.View.Version.Revision++
			fixture.frames <- frame
			if _, err := connection.Next(); err != nil {
				t.Fatal(err)
			}
			observed, failure := fixture.call(t, "native/observe", NativeFrontendRequest{PID: 911, Viewer: "panel"})
			if failure != nil || observed.Caller != test.want || observed.Thread != "thread" || observed.Displayed != test.displayed {
				t.Fatalf("displayed caller changed runtime routing or default filter: %+v failure=%v", observed, failure)
			}
		})
	}
}

func TestNativeFrontendObservationReadsCurrentSharedState(t *testing.T) {
	f := newFrontendFixture(t)
	f.open(t, 913)
	frame := Frame{Session: Session{Thread: "thread", Workspace: "/workspace"}, Attachment: 1, Review: &changes.ReviewView{Revision: 1, Workspace: "/workspace"}}
	if err := f.api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	before, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 913, Viewer: "panel"})
	if failure != nil {
		t.Fatal(failure)
	}
	var publication message
	if json.Unmarshal(before.Publication, &publication) != nil || len(publication.Review) == 0 || string(publication.Review) == "null" {
		t.Fatal("shared review missing")
	}
	if err := f.api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	unchanged, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 913, Viewer: "panel"})
	if failure != nil || unchanged.Cursor != before.Cursor {
		t.Fatal("unchanged owner state advanced observation")
	}
	frame.Review = nil
	if err := f.api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	after, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 913, Instance: before.Instance, Viewer: "panel", After: before.Cursor})
	publication = message{}
	if failure != nil || after.Cursor == before.Cursor || json.Unmarshal(after.Publication, &publication) != nil || string(publication.Review) != "null" {
		t.Fatalf("review removal was not published: %+v failure=%v", after, failure)
	}
}

func TestNativeFrontendPanelEncodingReuseAndUnchangedHeartbeat(t *testing.T) {
	f := newFrontendFixture(t)
	f.open(t, 914)
	f.open(t, 915)
	frame := Frame{Session: Session{Thread: "thread", Workspace: "/workspace", Title: strings.Repeat("title", 1000)}, Attachment: 1, Input: &Input{Reason: strings.Repeat("input", 10000)}}
	if err := f.api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	initial, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 914, Viewer: "diff"})
	if failure != nil {
		t.Fatal(failure)
	}
	f.api.mu.Lock()
	retained := f.api.nativePanel
	inputDeferred := f.api.sharedDeferred["input"]
	f.api.mu.Unlock()
	var panel message
	if json.Unmarshal(initial.Publication, &panel) != nil || panel.Input != nil || panel.Deferred.Input != nil || !inputDeferred {
		t.Fatal("panel encoding changed ordinary delivery deferrals or included unrelated input")
	}
	for _, request := range []NativeFrontendRequest{{PID: 914, Viewer: "agents"}, {PID: 915, Viewer: "diff"}} {
		observed, failure := f.call(t, "native/observe", request)
		if failure != nil || string(observed.Publication) != string(initial.Publication) {
			t.Fatalf("another observer lost shared publication: %+v %v", observed, failure)
		}
		f.api.mu.Lock()
		reused := f.api.nativePanel == retained && len(f.api.subscribers) == 0
		f.api.mu.Unlock()
		if !reused {
			t.Fatal("observer reencoded panels or materialized an unused shared subscription")
		}
	}
	f.hub.mu.Lock()
	var owner *rpcConnection
	for connection := range f.hub.connections {
		if connection.frontend.pid == 914 {
			owner = connection
		}
	}
	viewers := len(owner.frontend.viewers)
	f.hub.mu.Unlock()
	if viewers != 2 {
		t.Fatal("mounted panels lost independent viewer leases")
	}
	// The same observation path sees an expired long-poll deadline without a
	// wall-clock wait or a test-only production timeout setting.
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	value, failure := owner.observeFrontend(ctx, NativeFrontendRequest{After: initial.Cursor})
	if failure != nil {
		t.Fatal(failure)
	}
	heartbeat := value.(NativeObservation)
	if !heartbeat.Available || heartbeat.Cursor != initial.Cursor || heartbeat.Instance != initial.Instance || heartbeat.Attachment != initial.Attachment || heartbeat.Activation != initial.Activation || heartbeat.Displayed != initial.Displayed || heartbeat.Caller != initial.Caller || len(heartbeat.Publication) != 0 || heartbeat.Live != nil {
		t.Fatalf("unchanged deadline did not retain identities with a lightweight heartbeat: %+v", heartbeat)
	}
	f.api.mu.Lock()
	reused := f.api.nativePanel == retained
	f.api.mu.Unlock()
	if len(mustJSON(t, heartbeat))*2 >= len(mustJSON(t, initial)) || !reused {
		t.Fatal("unchanged deadline retransferred or reencoded shared panel state")
	}
	t.Logf("unchanged heartbeat %d bytes versus initial publication %d bytes", len(mustJSON(t, heartbeat)), len(mustJSON(t, initial)))
	frame.Session.Title = "changed"
	if err := f.api.Publish(frame); err != nil {
		t.Fatal(err)
	}
	changed, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 914, Instance: initial.Instance, Viewer: "diff", After: initial.Cursor})
	f.api.mu.Lock()
	reused = f.api.nativePanel == retained
	f.api.mu.Unlock()
	if failure != nil || changed.Cursor == initial.Cursor || len(changed.Publication) == 0 || reused {
		t.Fatal("changed owner revision reused stale detached panels")
	}
}

func TestNativeFrontendFileOpenObservation(t *testing.T) {
	f := newFrontendFixture(t)
	first := f.open(t, 916)
	initial, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 916, Viewer: "files"})
	if failure != nil {
		t.Fatal(failure)
	}
	if !initial.FileOpenSupported {
		t.Fatal("observation did not advertise file-open support")
	}
	if lease := <-f.requests; lease.Operation.Viewer.FileOpenExpires != 0 {
		t.Fatal("initial observer opted into file opening")
	}
	if _, failure = f.call(t, "native/observe", NativeFrontendRequest{PID: 916, Instance: initial.Instance, Viewer: "files", FileOpen: true}); failure != nil {
		t.Fatal(failure)
	}
	lease := <-f.requests
	if lease.Operation.Viewer.FileOpenExpires == 0 {
		t.Fatal("capability was not delivered")
	}
	frame := nativeTestFrame("thread", 1)
	frame.View.Displayed = frame.View.Main
	frame.FileOpen = &NativeFileOpen{ID: 1, Path: "/workspace/a b.go", First: 3, Last: 4}
	f.frames <- frame
	if _, err := first.Next(); err != nil {
		t.Fatal(err)
	}
	observed, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 916, Instance: initial.Instance, Viewer: "files", FileOpen: true, After: initial.Cursor})
	if failure != nil || observed.FileOpen == nil || *observed.FileOpen != *frame.FileOpen || observed.Cursor == initial.Cursor {
		t.Fatalf("file observation: %+v %v", observed, failure)
	}
	<-f.requests
	repeated, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 916, Instance: initial.Instance, Viewer: "files", FileOpen: true})
	if failure != nil || repeated.FileOpen == nil || *repeated.FileOpen != *observed.FileOpen || repeated.Cursor != observed.Cursor {
		t.Fatalf("selection not retained: %+v %v", repeated, failure)
	}
	<-f.requests
	if _, failure = f.call(t, "native/release", NativeFrontendRequest{PID: 916, Instance: initial.Instance, Viewer: "files"}); failure != nil {
		t.Fatal(failure)
	}
	if lease := <-f.requests; lease.Operation.Viewer.FileOpenExpires != 0 {
		t.Fatal("release retained capability")
	}
	if frame.remote().FileOpen != nil {
		t.Fatal("remote human received native intent")
	}
	if err := first.Activate(t.Context(), 916, 2); err != nil {
		t.Fatal(err)
	}
	<-f.requests
	reactivated, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 916, Instance: initial.Instance, Viewer: "files", FileOpen: true})
	if failure != nil || reactivated.FileOpen != nil {
		t.Fatalf("activation replayed previous selection: %+v %v", reactivated, failure)
	}
}

func TestNativeFrontendDiffSelectionAndContent(t *testing.T) {
	f := newFrontendFixture(t)
	first := f.open(t, 917)
	initial, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 917, Viewer: "diff"})
	if failure != nil || !initial.DiffOpenSupported {
		t.Fatalf("support: %+v %v", initial, failure)
	}
	if lease := <-f.requests; lease.Operation.Viewer.DiffOpenExpires != 0 {
		t.Fatal("unnegotiated capability")
	}
	observed, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 917, Instance: initial.Instance, Viewer: "diff", DiffOpen: true})
	if failure != nil {
		t.Fatal(failure)
	}
	if lease := <-f.requests; lease.Operation.Viewer.DiffOpenExpires == 0 {
		t.Fatal("missing diff lease")
	}
	frame := nativeTestFrame("thread", 1)
	frame.View.Displayed = frame.View.Main
	source := "@@ -1 +1 @@\n-old\n+exact\n" + strings.Repeat(" context\n", 1005)
	frame.DiffOpen = &NativeDiffOpen{ID: 1, Path: "a.go", Title: "Edit", Source: source}
	f.frames <- frame
	event, err := first.Next()
	if err != nil {
		t.Fatal(err)
	}
	if event.Frame.DiffOpen == nil || event.Frame.DiffOpen.Source != "" {
		t.Fatal("terminal wire leaked source")
	}
	observed, failure = f.call(t, "native/observe", NativeFrontendRequest{PID: 917, Instance: initial.Instance, Viewer: "diff", DiffOpen: true, After: observed.Cursor})
	if failure != nil || observed.DiffOpen == nil || observed.DiffOpen.ID != 1 || observed.DiffOpen.Source != "" {
		t.Fatalf("selection: %+v %v", observed, failure)
	}
	<-f.requests
	repeated, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 917, Instance: initial.Instance, Viewer: "diff", DiffOpen: true})
	if failure != nil || repeated.Cursor != observed.Cursor {
		t.Fatal("selection was not retained")
	}
	<-f.requests
	rpc, err := OpenRPC(t.Context(), f.server.Client(), f.server.URL+NativeRPCPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	ref := ContentReference{Kind: "native-diff", Attachment: observed.Attachment, Revision: 1, Seq: observed.Activation}
	read := func(ref ContentReference, offset uint64) (Lines, error) {
		body, _ := json.Marshal(&NativeFrontendRequest{PID: 917, Instance: initial.Instance, Content: &ref, Offset: &offset})
		var lines Lines
		err := rpc.Call(t.Context(), "native/content", RPCParams{Body: body}, &lines)
		return lines, err
	}
	lines, err := read(ref, 0)
	if err != nil || len(lines.Lines) != contentPageLines || lines.Next == nil {
		t.Fatalf("first page: %d %v", len(lines.Lines), err)
	}
	rest, err := read(ref, *lines.Next)
	if err != nil || rest.Next != nil || strings.Join(append(lines.Lines, rest.Lines...), "\n")+"\n" != source {
		t.Fatalf("continuation lost source: %v", err)
	}
	wrong := ref
	wrong.Revision++
	if _, err := read(wrong, 0); err == nil {
		t.Fatal("stale selection admitted")
	}
	wrong = ref
	wrong.Attachment = "other"
	if _, err := read(wrong, 0); err == nil {
		t.Fatal("foreign attachment admitted")
	}
	if frame.remote().DiffOpen != nil {
		t.Fatal("remote view retained diff selection")
	}
	frame.DiffOpen = &NativeDiffOpen{ID: 2, Path: "b.go", Title: "Edit", Source: "+replacement\n"}
	f.frames <- frame
	if _, err := first.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := read(ref, 0); err == nil {
		t.Fatal("replaced diff still readable")
	}
	ref.Revision = 2
	if lines, err := read(ref, 0); err != nil || len(lines.Lines) != 1 || lines.Lines[0] != "+replacement" {
		t.Fatalf("replacement: %+v %v", lines, err)
	}
	if err := first.Activate(t.Context(), 917, 2); err != nil {
		t.Fatal(err)
	}
	<-f.requests
	if _, err := read(ref, 0); err == nil {
		t.Fatal("old activation read admitted")
	}
	ref.Seq = 2
	if _, err := read(ref, 0); err == nil {
		t.Fatal("new activation read an excluded old selection")
	}
	reactivated, failure := f.call(t, "native/observe", NativeFrontendRequest{PID: 917, Instance: initial.Instance, Viewer: "diff", DiffOpen: true})
	if failure != nil || reactivated.DiffOpen != nil {
		t.Fatalf("activation replayed selection: %+v %v", reactivated, failure)
	}
	<-f.requests
	if _, failure := f.call(t, "native/release", NativeFrontendRequest{PID: 917, Instance: initial.Instance, Viewer: "diff"}); failure != nil {
		t.Fatal(failure)
	}
	if lease := <-f.requests; lease.Operation.Viewer.DiffOpenExpires != 0 {
		t.Fatal("release retained diff capability")
	}
}
