package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
	"testing"
)

func TestNativeClientPartialSubmissionMovesFocus(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	questionTestAsync(t, u, "batch", "First?", "Second?")
	c := nativeTestPromptClient(u)
	frame := c.event.Frame
	frame.Questions[0].Questions[0].Pending = false
	frame.Questions[0].Questions[0].State = "sending"
	c.accept(sessionapi.NativeEvent{Attachment: "client", Frame: frame})
	if c.prompts.questions.Current().ID != frame.Questions[0].Questions[1].ID {
		t.Fatal("focus stayed on submitted question")
	}
	// Navigation must remain available during the publication handoff too.
	c.prompts.questions.Index = 0
	q := frame.Questions[0]
	c.prompt(terminalsession.PromptInput{Version: frame.View.Version, Question: prompt.QuestionInput{Call: q.ID, Question: q.Questions[0].ID, Kind: prompt.InputNone}, Action: conversationui.PromptHide}, nil)
	if c.prompts.questions.Active != nil {
		t.Fatal("Esc was blocked on submitted question")
	}
}
