package sessioncontrol

import (
	"encoding/json/jsontext"
	"errors"
	"maps"
	"reflect"
	"testing"
)

type requestTransport struct {
	id          string
	err         error
	initialized int
	sends       []requestSend
	responses   []requestResponse
}

type requestSend struct {
	method  string
	params  any
	request bool
}

type requestResponse struct {
	id     jsontext.Value
	result any
}

func (s *requestTransport) Initialize() (string, error) {
	s.initialized++
	return s.id, s.err
}

func (s *requestTransport) Send(method string, params any, request bool) (string, error) {
	s.sends = append(s.sends, requestSend{method, params, request})
	return s.id, s.err
}

func (s *requestTransport) Respond(id jsontext.Value, result any) error {
	s.responses = append(s.responses, requestResponse{id, result})
	return s.err
}

func TestRequestsIssueAndConsume(t *testing.T) {
	type owner struct {
		label string
		turn  int
	}
	transport := &requestTransport{id: "init-7"}
	requests := NewRequests[owner](transport)
	if requests.Pending() {
		t.Fatal("new ledger has pending requests")
	}
	initialOwner := owner{"startup", 1}
	if id, err := requests.Initialize(initialOwner); id != "init-7" || err != nil {
		t.Fatalf("Initialize = %q, %v", id, err)
	}
	transport.id = "request-19"
	params := &struct{ ThreadID string }{"thread-a"}
	sendOwner := owner{"compact", 2}
	if id, err := requests.Send("thread/compact", params, "thread-a", sendOwner); id != "request-19" || err != nil {
		t.Fatalf("Send = %q, %v", id, err)
	}
	if transport.initialized != 1 || !reflect.DeepEqual(transport.sends, []requestSend{{"thread/compact", params, true}}) {
		t.Fatalf("transport calls: initialize=%d, sends=%#v", transport.initialized, transport.sends)
	}
	want := map[string]Request[owner]{
		"init-7":     {Method: "initialize", Owner: initialOwner},
		"request-19": {Method: "thread/compact", Scope: "thread-a", Owner: sendOwner},
	}
	if got := maps.Clone(requests.pending); !reflect.DeepEqual(got, want) {
		t.Fatalf("pending = %#v, want %#v", got, want)
	}
	if got, ok := requests.Take("unknown"); ok || got != (Request[owner]{}) {
		t.Fatalf("unknown response = %#v, %v", got, ok)
	}
	for _, id := range []string{"init-7", "request-19"} {
		if !requests.Pending() {
			t.Fatalf("ledger empty before taking %q", id)
		}
		if got, ok := requests.Take(id); !ok || got != want[id] {
			t.Fatalf("Take(%q) = %#v, %v; want %#v", id, got, ok, want[id])
		}
		if got, ok := requests.Take(id); ok || got != (Request[owner]{}) {
			t.Fatalf("repeated Take(%q) = %#v, %v", id, got, ok)
		}
	}
	if requests.Pending() {
		t.Fatal("consumed requests remain pending")
	}
}

func TestRequestsFailedIssuancePreservesExistingRequest(t *testing.T) {
	transport := &requestTransport{id: "existing"}
	requests := NewRequests[string](transport)
	if _, err := requests.Send("thread/read", nil, "thread-a", "existing owner"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("transport failed")
	transport.id, transport.err = "failed", failure
	if id, err := requests.Initialize("failed init"); id != "failed" || !errors.Is(err, failure) {
		t.Fatalf("failed Initialize = %q, %v", id, err)
	}
	params := &struct{ TurnID string }{"turn-b"}
	if id, err := requests.Send("turn/interrupt", params, "thread-b", "failed send"); id != "failed" || !errors.Is(err, failure) {
		t.Fatalf("failed Send = %q, %v", id, err)
	}
	wantCalls := []requestSend{{"thread/read", nil, true}, {"turn/interrupt", params, true}}
	if transport.initialized != 1 || !reflect.DeepEqual(transport.sends, wantCalls) {
		t.Fatalf("transport calls: initialize=%d, sends=%#v", transport.initialized, transport.sends)
	}
	want := map[string]Request[string]{"existing": {Method: "thread/read", Scope: "thread-a", Owner: "existing owner"}}
	if got := maps.Clone(requests.pending); !reflect.DeepEqual(got, want) || !requests.Pending() {
		t.Fatalf("ledger after failed issuance = %#v, want %#v", got, want)
	}
}

func TestRequestsNotificationsAndResponsesDoNotCorrelate(t *testing.T) {
	transport := &requestTransport{id: "notification-id"}
	requests := NewRequests[struct{}](transport)
	params := &struct{ Ready bool }{true}
	result := &struct{ Approved bool }{true}
	id := jsontext.Value(`"host-request-03"`)
	if err := requests.Notify("initialized", params); err != nil {
		t.Fatal(err)
	}
	if err := requests.Respond(id, result); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("write failed")
	transport.err = failure
	if err := requests.Notify("initialized", params); !errors.Is(err, failure) {
		t.Fatalf("failed Notify = %v", err)
	}
	if err := requests.Respond(id, result); !errors.Is(err, failure) {
		t.Fatalf("failed Respond = %v", err)
	}
	wantSends := []requestSend{{"initialized", params, false}, {"initialized", params, false}}
	wantResponses := []requestResponse{{id, result}, {id, result}}
	if transport.initialized != 0 || !reflect.DeepEqual(transport.sends, wantSends) || !reflect.DeepEqual(transport.responses, wantResponses) {
		t.Fatalf("transport calls: initialize=%d, sends=%#v, responses=%#v", transport.initialized, transport.sends, transport.responses)
	}
	if requests.Pending() || len(maps.Clone(requests.pending)) != 0 {
		t.Fatal("notification or response entered the ledger")
	}
}

func TestRequestsRetireAndForgetOnlyRemoveCorrelation(t *testing.T) {
	transport := &requestTransport{}
	requests := NewRequests[string](transport)
	for _, entry := range []struct{ id, scope, owner string }{
		{"a1", "thread-a", "old compact"},
		{"a2", "thread-a", "old status"},
		{"b1", "thread-b", "new compact"},
		{"b2", "thread-b", "new status"},
	} {
		transport.id = entry.id
		if _, err := requests.Send("thread/read", nil, entry.scope, entry.owner); err != nil {
			t.Fatal(err)
		}
	}
	requests.Retire("thread-a")
	requests.Retire("unknown")
	requests.Forget("b2")
	requests.Forget("unknown")
	want := map[string]Request[string]{"b1": {Method: "thread/read", Scope: "thread-b", Owner: "new compact"}}
	if got := maps.Clone(requests.pending); !reflect.DeepEqual(got, want) || !requests.Pending() {
		t.Fatalf("ledger after retirement = %#v, want %#v", got, want)
	}
	for _, id := range []string{"a1", "a2", "b2"} {
		if _, ok := requests.Take(id); ok {
			t.Fatalf("removed request %q still correlates", id)
		}
	}
	requests.Forget("b1")
	if requests.Pending() {
		t.Fatal("forgotten last request remains pending")
	}
	wantSends := []requestSend{
		{"thread/read", nil, true}, {"thread/read", nil, true},
		{"thread/read", nil, true}, {"thread/read", nil, true},
	}
	if transport.initialized != 0 || !reflect.DeepEqual(transport.sends, wantSends) || len(transport.responses) != 0 {
		t.Fatalf("retirement changed host calls: initialize=%d, sends=%#v, responses=%#v", transport.initialized, transport.sends, transport.responses)
	}
}
