package router

import (
	"slices"
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
	pending := slices.Concat(u.inputPayloads[u.Submission().ID].parts, u.unsent, u.queued, u.steerParts(u.Steers()))
	for _, call := range u.questions.calls {
		for _, question := range call.questions {
			pending = append(pending, composerDraft{Draft: question.editor.Snapshot.Draft})
		}
	}
	if u.currentQuestion() != nil {
		pending = append(pending, composerDraft{Draft: u.questions.parked.Snapshot.Draft})
	}
	if err := u.Editor.InsertSelection(description, text, source, draftValues(pending)); err != nil {
		u.setNotice(err.Error(), true)
		return
	}
	u.setNotice("", false)
	u.cursorColumn = nil
	u.pruneDraftImages()
	if q := u.currentQuestion(); q != nil {
		if !q.note {
			q.selected = len(q.choices)
		}
		q.done, q.skipped = false, false
	}
}
