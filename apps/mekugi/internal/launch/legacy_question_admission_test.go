package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"testing"
)

func TestLegacyIDlessQuestionIsNotAnswerable(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.restoreHistory([]binding.HistoryTurn{{ID: "old", Status: "completed", Items: []binding.ThreadItem{{ID: "legacy", Type: "agentMessage", Delivery: "async", Questions: []binding.Question{{Question: "Continue?", Options: []binding.QuestionOption{{Label: "Yes"}}}}}}}})
	call := u.questions.Calls[0]
	view := call.View()
	if view.Questions[0].Pending || view.Questions[0].State != "unavailable" {
		t.Fatal("legacy ID-less question misleadingly pending", view)
	}
	if !call.Resolved || len(apiPrompts(&u.attached, &u.Client).Questions) != 0 || len(nativePendingQuestions(u.attached.conversation)) != 0 {
		t.Fatal("unavailable-only batch still published as pending")
	}
	client := nativeTestPromptClient(u)
	client.autoOpenPrompts()
	if client.prompts.questions.Active != nil {
		t.Fatal("unavailable-only batch auto-opened")
	}
	input := sessionapi.NativeAnswers{Call: call.ID, Answers: []sessionapi.NativeQuestionAnswer{{Call: call.ID, Question: view.Questions[0].ID, Choice: 0}}}
	if refusal := nativeTestAnswers(t, u, input); refusal == "" || wire.Len() != 0 {
		t.Fatal("legacy answer accepted but cannot be routed", refusal)
	}
}

func TestLegacyMixedBatchKeepsIdentifiedQuestionPending(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.restoreHistory([]binding.HistoryTurn{{ID: "old", Status: "completed", Items: []binding.ThreadItem{{ID: "legacy", Type: "agentMessage", Delivery: "async", Questions: []binding.Question{{Question: "Old?"}, {ID: "authored-id", Question: "Answerable?", Options: []binding.QuestionOption{{Label: "Yes"}}}}}}}})
	call := u.questions.Calls[0]
	if call.Resolved || len(apiPrompts(&u.attached, &u.Client).Questions) != 1 || len(apiPrompts(&u.attached, &u.Client).Questions[0].Questions) != 1 {
		t.Fatal("mixed batch retired answerable sibling")
	}
	u.AwaitingTurn = true
	input := sessionapi.NativeAnswers{Call: call.ID, Answers: []sessionapi.NativeQuestionAnswer{{Call: call.ID, Question: "authored-id", Choice: 0}}}
	if refusal := nativeTestAnswers(t, u, input); refusal != "" {
		t.Fatal(refusal)
	}
	parts := u.attached.conversation.Unsent
	if len(parts) != 1 || len(binding.QuestionReplies(parts[0].Text)) != 1 || binding.QuestionReplies(parts[0].Text)[0].ID != "authored-id" {
		t.Fatal("authored ID not submitted")
	}
}
