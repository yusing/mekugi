package orchestrate

import (
	"encoding/json/jsontext"
	"testing"
)

func TestDeliveryRetainsDispatchAndAcknowledgement(t *testing.T) {
	s, workspace, _ := launchFixture(t)
	input := Delivery{ID: "host-call", From: "main", Target: "child", Message: "continue"}
	if _, dispatch, err := s.BeginDelivery(t.Context(), workspace, "main", input); err == nil || dispatch {
		t.Fatal("delivery preceded confirmed launch")
	}
	if _, _, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "work", jsontext.Value(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordThread(t.Context(), workspace, "main", "batch", "child", jsontext.Value(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTurn(t.Context(), workspace, "main", "batch", "child", "first"); err != nil {
		t.Fatal(err)
	}
	if d, dispatch, err := s.BeginDelivery(t.Context(), workspace, "main", input); err != nil || !dispatch || d.State != "dispatching" {
		t.Fatal("dispatch intent", d, dispatch, err)
	}
	s = &Store{Directory: s.Directory}
	if d, dispatch, err := s.BeginDelivery(t.Context(), workspace, "main", input); err != nil || dispatch || d.State != "dispatching" {
		t.Fatal("reopen repeated unconfirmed effect", d, dispatch, err)
	}
	changed := input
	changed.Message = "changed"
	if _, dispatch, err := s.BeginDelivery(t.Context(), workspace, "main", changed); err == nil || dispatch {
		t.Fatal("changed call accepted")
	}
	if _, err := s.RecordDelivery(t.Context(), workspace, "main", input.ID, "delivered", "", ""); err == nil {
		t.Fatal("accepted without turn identity")
	}
	if d, err := s.RecordDelivery(t.Context(), workspace, "main", input.ID, "delivered", "followup-turn", ""); err != nil || d.State != "delivered" {
		t.Fatal(d, err)
	}
	if d, dispatch, err := s.BeginDelivery(t.Context(), workspace, "main", input); err != nil || dispatch || d.TurnID != "followup-turn" {
		t.Fatal("delivery repeated", d, dispatch, err)
	}
	queued := Delivery{ID: "queued-call", From: "main", Target: "child", Message: "deferred", Deferred: true}
	if d, dispatch, err := s.BeginDelivery(t.Context(), workspace, "main", queued); err != nil || dispatch || d.State != "queued" {
		t.Fatal("queue started dispatch", d, dispatch, err)
	}
	s = &Store{Directory: s.Directory}
	for _, state := range []string{"canceled", "rejected", "delivered"} {
		input.ID = state
		d, text, dispatch, err := s.BeginTurnDelivery(t.Context(), workspace, "main", input)
		if err != nil || !dispatch || text != "deferred\ncontinue" {
			t.Fatal("queue was lost or reordered", d, text, dispatch, err)
		}
		// Reopening after intent does not redispatch or release the claim.
		s = &Store{Directory: s.Directory}
		if _, _, dispatch, err := s.BeginTurnDelivery(t.Context(), workspace, "main", input); err != nil || dispatch {
			t.Fatal("claimed queue repeated after reopen", dispatch, err)
		}
		turn := ""
		if state == "delivered" {
			turn = "next-turn"
		}
		if _, err := s.RecordDelivery(t.Context(), workspace, "main", d.ID, state, turn, ""); err != nil {
			t.Fatal(err)
		}
		got, _, err := s.BeginDelivery(t.Context(), workspace, "main", queued)
		want := "queued"
		if state == "delivered" {
			want = state
		}
		if err != nil || got.State != want || got.TurnID != turn {
			t.Fatal("queue acknowledgement", got, err)
		}
	}
	queued.ID = "uncertain-queued"
	if _, _, err := s.BeginDelivery(t.Context(), workspace, "main", queued); err != nil {
		t.Fatal(err)
	}
	input.ID = "uncertain-dispatch"
	if _, _, _, err := s.BeginTurnDelivery(t.Context(), workspace, "main", input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordDelivery(t.Context(), workspace, "main", input.ID, "uncertain", "", "connection closed"); err != nil {
		t.Fatal(err)
	}
	s = &Store{Directory: s.Directory}
	input.ID = "later-dispatch"
	if _, text, dispatch, err := s.BeginTurnDelivery(t.Context(), workspace, "main", input); err != nil || !dispatch || text != "continue" {
		t.Fatal("uncertain input was resent", text, dispatch, err)
	}
	input.ID, input.From = "foreign-call", "foreign"
	if _, dispatch, err := s.BeginDelivery(t.Context(), workspace, "main", input); err == nil || dispatch {
		t.Fatal("unconfirmed caller accepted")
	}
}
