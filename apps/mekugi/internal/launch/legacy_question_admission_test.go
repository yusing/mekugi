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
	input := sessionapi.NativeAnswers{Call: call.ID, Answers: []sessionapi.NativeQuestionAnswer{{Call: call.ID, Question: view.Questions[0].ID, Choice: 0}}}
	if refusal := nativeTestAnswers(t, u, input); refusal == "" || wire.Len() != 0 {
		t.Fatal("legacy answer accepted but cannot be routed", refusal)
	}
}
