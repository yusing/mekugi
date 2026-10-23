package control

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestInstructionProvenanceBatchConflictIsAtomic(t *testing.T) {
	s, _ := fixture(t)
	owner, err := s.owner("root")
	must(t, err)
	before := owner.state.Load()
	part := guidance.Fragment{Thread: "root", Item: "item", Index: 1, Kind: "developer", Text: "first"}
	conflict := part
	conflict.Text = "second"
	if err := s.RetainInstructions(t.Context(), "root", "/new-path", []guidance.Fragment{part, conflict}); err == nil {
		t.Fatal("conflicting fragments in one batch were admitted")
	}
	if owner.state.Load() != before {
		t.Fatal("rejected batch replaced the publication")
	}
	must(t, s.RetainInstructions(t.Context(), "root", "/accepted-path", []guidance.Fragment{conflict}))
	member := owner.state.Load().Members["root"]
	if member.Path != "/accepted-path" || !slices.Equal(member.Instructions, []guidance.Fragment{conflict}) {
		t.Fatalf("rejected batch leaked state: %+v", member)
	}
}

func TestInstructionProvenanceConcurrentConflict(t *testing.T) {
	s, _ := fixture(t)
	parts := []guidance.Fragment{
		{Thread: "root", Item: "item", Index: 0, Kind: "developer", Text: "first"},
		{Thread: "root", Item: "item", Index: 0, Kind: "developer", Text: "second"},
	}
	start := make(chan struct{})
	results := make(chan int, len(parts))
	for i, part := range parts {
		go func() {
			<-start
			if err := s.RetainInstructions(t.Context(), "root", "/instructions", []guidance.Fragment{part}); err != nil {
				results <- -1
				return
			}
			results <- i
		}()
	}
	close(start)
	winner, successes := -1, 0
	for range parts {
		if result := <-results; result >= 0 {
			winner = result
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful conflicting admissions = %d, want 1", successes)
	}
	state, err := s.Snapshot("root")
	must(t, err)
	if !slices.Equal(state.Members["root"].Instructions, []guidance.Fragment{parts[winner]}) {
		t.Fatalf("retained provenance differs from winning admission: %+v", state.Members["root"].Instructions)
	}
}

func TestInstructionRetryReusesPublishedState(t *testing.T) {
	s, session := fixture(t)
	parts := []guidance.Fragment{{Thread: "root", Item: "item", Kind: "developer", Text: "instruction"}}
	must(t, s.RetainInstructions(t.Context(), "root", "/instructions", parts))
	owner, err := s.owner("root")
	must(t, err)
	before := owner.state.Load()
	// The retry must finish while another caller holds the storage transaction.
	must(t, session.Store.Transaction(t.Context(), session.Writes, func(*history.Transaction) error {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		return s.RetainInstructions(ctx, "root", "/instructions", parts)
	}))
	if owner.state.Load() != before {
		t.Fatal("unchanged instructions replaced the publication")
	}
}

func TestInstructionAdmissionPreservesPriorPublication(t *testing.T) {
	s, _ := fixture(t)
	first := guidance.Fragment{Thread: "root", Item: "item", Index: 0, Kind: "developer", Text: "first"}
	must(t, s.RetainInstructions(t.Context(), "root", "/first", []guidance.Fragment{first}))
	owner, err := s.owner("root")
	must(t, err)
	before := owner.state.Load()
	prior := before.Members["root"]
	instructions := prior.Instructions
	second := first
	second.Index, second.Text = 1, "second"
	must(t, s.RetainInstructions(t.Context(), "root", "/second", []guidance.Fragment{first, second}))
	after := owner.state.Load()
	if after == before || after.Members["root"] == prior {
		t.Fatal("new instruction admission reused the prior publication or member")
	}
	if prior.Path != "/first" || !slices.Equal(prior.Instructions, []guidance.Fragment{first}) || !slices.Equal(instructions, []guidance.Fragment{first}) {
		t.Fatalf("prior member or instructions changed: %+v, %+v", prior, instructions)
	}
	if member := after.Members["root"]; member.Path != "/second" || !slices.Equal(member.Instructions, []guidance.Fragment{first, second}) {
		t.Fatalf("new admission was not published: %+v", member)
	}
}
