package preview

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/contracts/scope"
)

func TestLiveDiffSingleSubscriberLifecycle(t *testing.T) {
	broker := newSession(t.Context())
	old, _ := broker.Subscribe()
	current, _ := broker.Subscribe()
	broker.Unsubscribe(old)
	publishTurn(broker, true)
	if len(old.Events()) != 0 || len(current.Events()) != 1 {
		t.Fatal("publication must reach only the replacement mailbox")
	}
	if event := <-current.Events(); event.Kind != "turn" || event.Status != "active" {
		t.Fatalf("replacement event = %+v", event)
	}
	broker.Unsubscribe(current)
	publishTurn(broker, false)
	if len(current.Events()) != 0 {
		t.Fatal("detached mailbox received publication")
	}
	next, _ := broker.Subscribe()
	if event := <-next.Events(); event.Status != "completed" || event.TurnRevision != 2 {
		t.Fatalf("retained state = %+v", event)
	}
}

func TestLiveDiffPreviewMailboxOverflowResync(t *testing.T) {
	broker := newSession(t.Context())
	setScope(broker, Scope{Workspaces: map[string]map[string]bool{"/workspace": {"thread": true}}})
	sub, _ := broker.Subscribe()
	// Distinct removals cannot coalesce, so a stalled UI must resynchronize.
	for i := range 33 {
		broker.Update(Preview{ID: fmt.Sprint(i)}, true)
	}
	select {
	case <-sub.Gap():
	default:
		t.Fatal("preview overflow did not request resynchronization")
	}
	completed := previewViewFixture("completed", 1)
	completed.Complete, completed.Evaluated = true, true
	broker.Update(completed, false)
	broker.Update(previewViewFixture("retained", 1), false)
	next, ids := broker.Subscribe()
	if !slices.Equal(ids, []string{"completed", "retained"}) {
		t.Fatalf("resync membership = %v", ids)
	}
	broker.Unsubscribe(sub)
	batch := broker.TakePreviews(next)
	if len(batch) != 2 || !batch[0].Resync || !batch[1].Resync || batch[0].Preview == nil || !batch[0].Preview.Complete || batch[1].Preview == nil || batch[1].Preview.ID != "retained" {
		t.Fatalf("resynchronization lost current display state: %+v", batch)
	}
	broker.Update(Preview{ID: "retained"}, true)
	batch = broker.TakePreviews(next)
	if len(batch) != 1 || batch[0].Preview == nil || batch[0].Preview.Workspace != "" {
		t.Fatalf("resynchronized mailbox missed preview removal: %+v", batch)
	}
}

func TestLiveDiffTurnStatePublicationAndReplay(t *testing.T) {
	broker := newSession(t.Context())
	setScope(broker, Scope{Workspaces: map[string]map[string]bool{"/work": {"root": true}}})
	publishTurn(broker, true)
	sub, _ := broker.Subscribe()
	if event := <-sub.Events(); event.Kind != "turn" || event.Status != "active" || event.TurnRevision != 1 {
		t.Fatalf("retained turn = %+v", event)
	}
	publishTurn(broker, false)
	publishTurn(broker, false)
	for revision := uint64(2); revision <= 3; revision++ {
		if event := <-sub.Events(); event.Kind != "turn" || event.Status != "completed" || event.TurnRevision != revision {
			t.Fatalf("completion event = %+v", event)
		}
	}
	replay, _ := broker.Subscribe()
	if event := <-replay.Events(); event.Status != "completed" || event.TurnRevision != 3 {
		t.Fatalf("replayed completion = %+v", event)
	}
}

func previewViewFixture(id string, rows int) Preview {
	var diff strings.Builder
	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&diff, "+stream_%04d\n", i)
	}
	return Preview{ID: id, Workspace: "/workspace", Thread: "thread", Input: diff.String()}
}

func setScope(service *Session, scope Scope) { service.Enable(); service.SetScope(scope) }

func publishTurn(service *Session, active bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.publishTurnLocked(active)
}

func TestAdmittedScopeIsImmutable(t *testing.T) {
	service := newSession(t.Context())
	scope := Scope{Workspaces: map[string]map[string]bool{"/workspace": {"thread": true}}}
	service.SetScope(scope)
	scope.Workspaces["/workspace"]["foreign"] = true
	foreign := previewViewFixture("foreign", 1)
	foreign.Thread = "foreign"
	service.Update(foreign, false)
	service.Update(previewViewFixture("authorized", 1), false)
	if current := service.Current(); len(current) != 1 || current[0].ID != "authorized" {
		t.Fatalf("mutated caller scope admitted preview: %+v", current)
	}
}

func newSession(ctx context.Context) *Session {
	return New(changecapture.ReadFile).Attach(ctx, new(scope.Store))
}
