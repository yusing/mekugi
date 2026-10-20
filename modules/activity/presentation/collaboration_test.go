package presentation

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestCollaborationAdmissionAndRecipientIdentity(t *testing.T) {
	item := observation.Item{ID: "spawn", Tool: "spawnAgent", SenderThreadID: "parent", ReceiverThreadIDs: []string{"second", "first"}, ReasoningEffort: "high", Prompt: "\n body\n"}
	got, confirmed := ProjectCollaboration(item, ObservedCollaboration)
	want := []CollaborationDelivery{
		{Kind: "start", ID: "spawn\x00second", AssignmentID: "spawn", From: "parent", To: "second", Text: "\n body\n", Effort: "high"},
		{Kind: "start", ID: "spawn\x00first", AssignmentID: "spawn", From: "parent", To: "first", Text: "\n body\n", Effort: "high"},
	}
	if !confirmed || !reflect.DeepEqual(got, want) {
		t.Fatalf("spawn facts: %+v, confirmed=%v", got, confirmed)
	}
	got, confirmed = ProjectCollaboration(item, HistoricalCollaboration)
	if confirmed || len(got) != 2 || got[0].Kind != "error" || got[0].Text != "Historical spawnAgent: unknown status; delivery not confirmed" {
		t.Fatalf("unknown history claimed delivery: %+v, confirmed=%v", got, confirmed)
	}
	item.Status, item.Prompt = "completed", " \n"
	got, confirmed = ProjectCollaboration(item, HistoricalCollaboration)
	if !confirmed || len(got) != 2 || got[0].Kind != "start" || got[0].AssignmentID != "" {
		t.Fatalf("blank spawn lost identity or invented assignment: %+v", got)
	}
	item.Tool = "sendMessage"
	if got, confirmed := ProjectCollaboration(item, ObservedCollaboration); len(got) != 0 || !confirmed {
		t.Fatalf("blank message lost participant admission: %+v, confirmed=%v", got, confirmed)
	}
	item.Status = "failed"
	if got, confirmed := ProjectCollaboration(item, ObservedCollaboration); len(got) != 0 || confirmed {
		t.Fatalf("failed message claimed delivery: %+v, confirmed=%v", got, confirmed)
	}
}
