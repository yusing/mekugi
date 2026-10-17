package sessionreview

import (
	"testing"
)

func TestLiveDiffTurnEventValidation(t *testing.T) {
	for _, status := range []string{"active", "completed"} {
		if err := validateEvent(Event{Kind: "turn", Status: status, TurnRevision: 1}); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
	}
	if err := validateEvent(Event{Kind: "turn", Status: "unknown", TurnRevision: 1}); err == nil {
		t.Fatal("accepted unknown turn status")
	}
	if err := validateEvent(Event{Kind: "turn", Status: "active"}); err == nil {
		t.Fatal("accepted turn event without revision")
	}
}
