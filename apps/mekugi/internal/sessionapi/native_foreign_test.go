package sessionapi

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

const (
	foreignSourceRoot  = "11111111-1111-4111-8111-111111111111"
	foreignSourceActor = "22222222-2222-4222-8222-222222222222"
	foreignTargetRoot  = "33333333-3333-4333-8333-333333333333"
	foreignTargetActor = "44444444-4444-4444-8444-444444444444"
)

func nativeForeignPair(t *testing.T) (*Hub, *Server, *Server, appserver.AgentForeignRequest) {
	t.Helper()
	hub := NewHub(t.Context(), nil)
	root := func(id string) *managedSession {
		server := &Server{live: true}
		runtime := &managedSession{ctx: t.Context(), server: server}
		hub.running[runtime] = struct{}{}
		hub.sessions[id] = &listedSession{server: server, runtime: runtime}
		return runtime
	}
	source, target := root(foreignSourceRoot), root(foreignTargetRoot)
	actor := func(id string, owner *managedSession) {
		server := &Server{live: true}
		hub.sessions[id] = &listedSession{server: server, runtime: owner}
	}
	actor(foreignSourceActor, source)
	actor(foreignTargetActor, target)
	request := appserver.AgentForeignRequest{
		Source: appserver.AgentForeignOrigin{
			RootThreadID: foreignSourceRoot, ActorID: foreignSourceActor,
			AgentPath: "/root/source", TurnID: "original-turn", CallID: "original-call",
		},
		TargetThreadID: foreignTargetActor,
		Operation:      appserver.AgentForeignOperation{Type: "inspect"},
	}
	return hub, source.server, target.server, request
}

func TestNativeForeignRoutesEveryOperationThroughBorrowedNativeOwner(t *testing.T) {
	for _, operation := range []string{"message", "sideQuestion", "list", "inspect"} {
		t.Run(operation, func(t *testing.T) {
			hub, source, destination, request := nativeForeignPair(t)
			request.Operation.Type = operation
			text := "original @recipient-source.txt"
			want := appserver.AgentForeignResponse{Type: "inspected", Agent: &appserver.AgentObservation{ThreadID: foreignTargetActor}}
			switch operation {
			case "message":
				request.Operation.Message = &text
				want = appserver.AgentForeignResponse{Type: "messageAccepted", TargetThreadID: foreignTargetActor, DeliveryID: "55555555-5555-4555-8555-555555555555"}
			case "sideQuestion":
				request.Operation.Question = &text
				answer := "independent native answer"
				want = appserver.AgentForeignResponse{Type: "sideQuestionAnswered", TargetThreadID: foreignTargetActor, Answer: &answer}
			case "list":
				prefix := "worker"
				request.Operation.PathPrefix = &prefix
				want = appserver.AgentForeignResponse{Type: "listed", Tree: &appserver.AgentTreeObservation{RootThreadID: foreignTargetRoot, Agents: []appserver.AgentObservation{{ThreadID: foreignTargetActor}}}}
			}
			calls := 0
			destination.BindNativeForeign(func(_ context.Context, got appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error) {
				calls++
				if !reflect.DeepEqual(got, request) {
					t.Fatalf("native request changed in forwarding: %#v", got)
				}
				if !hub.mu.TryLock() {
					t.Fatal("Hub lock held across native RPC")
				}
				hub.mu.Unlock()
				if !destination.mu.TryLock() {
					t.Fatal("endpoint lock held across native RPC")
				}
				destination.mu.Unlock()
				return want, nil
			})
			hub.sessions[foreignTargetActor].server.BindNativeForeign(func(context.Context, appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error) {
				t.Fatal("foreign request used a child endpoint instead of the native owner")
				return appserver.AgentForeignResponse{}, nil
			})
			for range 2 {
				got, err := hub.RouteForeign(t.Context(), source, request)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("got %#v, %v; want unchanged native result", got, err)
				}
			}
			if calls != 2 || len(hub.sessions) != 4 {
				t.Fatalf("host deduplicated native operations or changed membership: calls=%d sessions=%d", calls, len(hub.sessions))
			}
		})
	}
}

func TestNativeForeignRejectsWrongConnectionColdAliasAndControlTargets(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Hub, **Server, *appserver.AgentForeignRequest)
	}{
		{"wrong connection", func(_ *Hub, source **Server, _ *appserver.AgentForeignRequest) { *source = &Server{} }},
		{"actor endpoint is not root authority", func(h *Hub, source **Server, _ *appserver.AgentForeignRequest) {
			*source = h.sessions[foreignSourceActor].server
		}},
		{"forged source root", func(_ *Hub, _ **Server, r *appserver.AgentForeignRequest) { r.Source.RootThreadID = foreignTargetRoot }},
		{"forged source actor", func(_ *Hub, _ **Server, r *appserver.AgentForeignRequest) { r.Source.ActorID = foreignTargetActor }},
		{"unknown source", func(_ *Hub, _ **Server, r *appserver.AgentForeignRequest) {
			r.Source.ActorID = "55555555-5555-4555-8555-555555555555"
		}},
		{"cold target", func(h *Hub, _ **Server, _ *appserver.AgentForeignRequest) { delete(h.sessions, foreignTargetActor) }},
		{"alias", func(_ *Hub, _ **Server, r *appserver.AgentForeignRequest) { r.TargetThreadID = "worker" }},
		{"same tree", func(_ *Hub, _ **Server, r *appserver.AgentForeignRequest) { r.TargetThreadID = foreignSourceActor }},
		{"stopped target", func(h *Hub, _ **Server, _ *appserver.AgentForeignRequest) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			h.sessions[foreignTargetActor].endpointContext = ctx
		}},
		{"stopped source", func(h *Hub, _ **Server, _ *appserver.AgentForeignRequest) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			h.sessions[foreignSourceRoot].endpointContext = ctx
		}},
		{"stopping hub", func(h *Hub, _ **Server, _ *appserver.AgentForeignRequest) { h.stopping = true }},
	}
	for _, operation := range []string{"spawn", "followupTask", "interrupt", "compact", "resume", "wait"} {
		cases = append(cases, struct {
			name   string
			change func(*Hub, **Server, *appserver.AgentForeignRequest)
		}{operation, func(_ *Hub, _ **Server, r *appserver.AgentForeignRequest) { r.Operation.Type = operation }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub, source, destination, request := nativeForeignPair(t)
			destination.BindNativeForeign(func(context.Context, appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error) {
				t.Fatal("unauthorized request reached native recipient")
				return appserver.AgentForeignResponse{}, nil
			})
			tc.change(hub, &source, &request)
			if _, err := hub.RouteForeign(t.Context(), source, request); err == nil {
				t.Fatal("invalid foreign route accepted")
			}
		})
	}
}

func TestNativeForeignRetirementCancelsBorrowedCallAndTransportFailureStaysUnknown(t *testing.T) {
	hub, source, destination, request := nativeForeignPair(t)
	lifetime, retire := context.WithCancel(t.Context())
	hub.sessions[foreignTargetActor].endpointContext = lifetime
	entered := make(chan struct{})
	destination.BindNativeForeign(func(ctx context.Context, _ appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error) {
		close(entered)
		<-ctx.Done()
		return appserver.AgentForeignResponse{}, ctx.Err()
	})
	result := make(chan error, 1)
	go func() {
		_, err := hub.RouteForeign(t.Context(), source, request)
		result <- err
	}()
	<-entered
	retire()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("retired recipient call: %v", err)
	}

	hub, source, destination, request = nativeForeignPair(t)
	lost := errors.New("destination receipt unavailable")
	destination.BindNativeForeign(func(context.Context, appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error) {
		return appserver.AgentForeignResponse{}, lost
	})
	response, err := hub.RouteForeign(t.Context(), source, request)
	if !errors.Is(err, lost) || response.Type != "" || response.Accepted != nil {
		t.Fatalf("transport loss fabricated native acceptance: %#v, %v", response, err)
	}
}
