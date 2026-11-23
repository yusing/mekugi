package launch

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"testing"
)

func TestPendingQuestionImagesRebaseReplyReferences(t *testing.T) {
	call := &prompt.Call{ID: "batch", Questions: []prompt.Question{{ID: "q1"}, {ID: "q2"}}}
	for i := range call.Questions {
		q := &call.Questions[i]
		q.Editor.Snapshot.Draft = composer.Draft{Text: "image", Images: []composer.Token{{Start: 0, End: 5, Path: []string{"first.png", "second.png"}[i]}}}
		q.Complete("image", false, false)
	}
	attached := questionAnswerAttachments(call, nil, nil)
	text := conversationbinding.QuestionReplies(call)
	call.Submitted()
	call.Questions[0].Outcome = "answered"
	parts := pendingQuestionReplies([]conversationmodel.Input{{Draft: composer.Draft{Text: text, AnswerImages: attached.AnswerImages}, Question: call, QuestionIDs: []string{"q1", "q2"}}})
	if len(parts) != 1 || len(parts[0].AnswerImages) != 1 || parts[0].AnswerImages[0] != "second.png" {
		t.Fatal(parts)
	}
	answers := binding.QuestionReplies(parts[0].Text)
	if len(answers) != 1 || answers[0].Other != "[Image 1]" {
		t.Fatalf("image reference not rebased: %+v", answers)
	}
}
