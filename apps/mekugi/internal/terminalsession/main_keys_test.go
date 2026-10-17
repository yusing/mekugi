package terminalsession

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestMainKeyContinuationTargetsLatestPublication(t *testing.T) {
	for _, dock := range []string{"question", "meta-menu"} {
		t.Run(dock, func(t *testing.T) {
			var main Main
			var prompts Prompts
			transcript := activityui.NewView()
			view := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 2, Revision: 3}, Draft: conversationmodel.DraftView{Revision: 4, Available: true}}
			key := "x"
			if dock == "question" {
				call := &prompt.Call{ID: "ask", Thread: "main", Questions: []prompt.Question{{ID: "choice"}}}
				var questions prompt.Questions
				questions.Add(call, false)
				questions.Open(call)
				view.Questions = questions.View("main")
				view.Draft = conversationmodel.DraftView{}
				view.PromptDraft = conversationmodel.PromptDraftView{Call: "ask", Question: "choice", Revision: 4}
			} else {
				key = "\x1bx"
				view.Menu = &sessionpresentation.MenuView{ID: 7, Kind: sessionpresentation.MenuSkills}
			}
			main.Update(view)
			input := main.BeginKey(key, &prompts, transcript, true)
			if input.Next == nil || dock == "question" && input.Prompt == nil || dock == "meta-menu" && (input.Menu == nil || input.Menu.Action.Kind != sessionpresentation.MenuDismiss || input.Menu.Handled) {
				t.Fatalf("first effect did not retain its continuation: %+v", input)
			}
			view.Version.Revision++
			view.Draft.Revision, view.PromptDraft.Revision = 5, 5
			view.Menu = nil
			main.Update(view)
			for range 3 {
				if input.Next == nil {
					break
				}
				input = main.ContinueKey(*input.Next, &prompts, transcript, true)
			}
			if input.Composer == nil || input.Composer.Editor.Version != view.Version || input.Composer.Editor.Target != view.EditorTarget() || input.Composer.Editor.Action.Text != "x" {
				t.Fatalf("continuation reused an old target or key framing: %+v", input)
			}
		})
	}
}
