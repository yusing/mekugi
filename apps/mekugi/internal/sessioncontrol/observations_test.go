package sessioncontrol

import (
	"errors"
	"testing"
)

func TestGenerationObservationsRemainRequestScoped(t *testing.T) {
	var observations Observations
	var first, second Generation
	observations.SetToolInput(&first, "", "main", "item", true)
	observations.SetToolInput(&second, "/workspace", "main", "item", true)
	observations.EndGeneration(&first)
	if !observations.EmittingToolInput("/workspace", "main") || observations.EmittingToolInput("/other", "main") || observations.EmittingToolInput("/workspace", "child") {
		t.Fatal("ending one request changed another request's generation scope")
	}
	observations.SetToolInput(&second, "/workspace", "main", "item", false)
	if observations.EmittingToolInput("/workspace", "main") {
		t.Fatal("completed tool input still blocks question answers")
	}
}

func TestApprovalFeedbackFollowsSuccessfulDelivery(t *testing.T) {
	var observations Observations
	failure := errors.New("answer rejected")
	if err := observations.AnswerWithFeedback("main", "turn", "undelivered", func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("delivery error = %v", err)
	}
	if observations.Feedback("main", "turn") != "" {
		t.Fatal("failed answer published feedback")
	}
	if err := observations.AnswerWithFeedback("main", "turn", "reason", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if observations.Feedback("main", "turn") != "reason" || observations.Feedback("child", "turn") != "" {
		t.Fatal("feedback was lost or escaped its thread")
	}
	observations.ClearFeedback("main", "other")
	if observations.Feedback("main", "turn") != "reason" {
		t.Fatal("unrelated turn removed feedback")
	}
	observations.ClearFeedback("main", "turn")
	if observations.Feedback("main", "turn") != "" {
		t.Fatal("completed turn retained feedback")
	}
}
