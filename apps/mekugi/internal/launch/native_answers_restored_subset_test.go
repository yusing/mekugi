package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"testing"
)

func TestNativeAnswersDoNotResendRestoredSibling(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.Turn = "turn"
	questionTestAsync(t, u, "batch", "First?", "Second?")
	call := u.questions.Calls[0]
	call.Questions[0].Selected = len(call.Questions[0].Choices)
	call.Questions[0].Complete("restored answer", false, false)
	u.AwaitingTurn = true
	input := sessionapi.NativeAnswers{Call: call.ID, Answers: []sessionapi.NativeQuestionAnswer{{Call: call.ID, Question: call.Questions[1].ID, Choice: 0}}}
	if refusal := nativeTestAnswers(t, u, input); refusal != "" {
		t.Fatal(refusal)
	}
	parts := u.attached.conversation.Unsent
	if len(parts) != 1 {
		t.Fatalf("queued inputs: %d", len(parts))
	}
	answers := binding.QuestionReplies(parts[0].Text)
	if len(answers) != 1 || answers[0].ID != call.Questions[1].ID || call.Questions[0].Sent {
		t.Fatalf("restored answer resent: %+v", answers)
	}
}
