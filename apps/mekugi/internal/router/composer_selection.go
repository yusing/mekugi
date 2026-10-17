package router

import (
	"slices"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

// composer.Token is a screen selection mentioned as one composer token.
// The draft shows only its concise label; the quoted text travels in the
// submitted attachment envelope, so history, forks and resume keep it.

// insertSelection mentions text at the caret as one undoable token. A label
// already in the draft or pending input gains a counter, so the queued, steered
// or restored input that joins them still names one quote per mention.
func (u *appServerUI) insertSelection(description, text, source string) {
	if text == "" {
		return
	}
	pending := slices.Concat(u.conversation().Submitted[u.Submission().ID].Parts, u.conversation().Unsent, u.conversation().Queued, u.conversation().Parts(u.Steers()))
	for _, call := range u.questionCalls() {
		for i, question := range call.Questions {
			draft := question.Editor.Snapshot.Draft
			if question.IsSecret {
				draft = call.secrets[i].Snapshot.Draft
			}
			pending = append(pending, conversationmodel.Input{Draft: draft})
		}
	}
	if u.currentQuestion() != nil {
		pending = append(pending, conversationmodel.Input{Draft: u.promptEditor.Parked.Snapshot.Draft})
	}
	if err := u.Editor.InsertSelection(description, text, source, conversationmodel.Drafts(pending)); err != nil {
		u.setNotice(err.Error(), true)
		return
	}
	u.setNotice("", false)
	u.mainView.CursorColumn = nil
	u.pruneDraftImages()
	if q := u.currentQuestion(); q != nil {
		q.Edit(!q.Note)
	}
}
