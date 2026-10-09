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
	input.ID, input.From = "foreign-call", "foreign"
	if _, dispatch, err := s.BeginDelivery(t.Context(), workspace, "main", input); err == nil || dispatch {
		t.Fatal("unconfirmed caller accepted")
	}
}
