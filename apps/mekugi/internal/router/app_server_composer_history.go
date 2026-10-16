package router

import (
	"os"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
)

type composerDraft struct {
	composer.Draft
	compact      sessioncontrol.CompactIntent
	questionCall *nativeQuestionCall
}

func (u *appServerUI) rememberInput(draft composerDraft) {
	if draft.questionCall == nil && len(questionReplies(draft.Text)) == 0 {
		u.Editor.Remember(draft.Draft)
	}
}
func (u *appServerUI) recallInput(backward bool) {
	if u.Editor.Recall(backward) {
		u.cursorColumn = nil
		u.setNotice("", false)
		u.pruneDraftImages()
	}
}
func (u *appServerUI) draftSnapshot() composerDraft { return composerDraft{Draft: u.Draft.Editable()} }
func (u *appServerUI) loadDraft(d composerDraft)    { u.Draft = d.Draft.Editable() }
func (u *appServerUI) recordDraft() {
	u.Editor.Record()
	u.setNotice("", false)
	u.pruneDraftImages()
}
func (u *appServerUI) undoDraft(redo bool) {
	u.Editor.Undo(redo)
	u.cursorColumn = nil
	u.setNotice("", false)
}

// Images referenced by undo/redo remain usable. Submission transfers file
// lifetime to Codex; only this session's never-submitted files are reclaimed.
func (u *appServerUI) pruneDraftImages() {
	used := make(map[string]bool)
	for _, image := range u.Images {
		used[image.Path] = true
	}
	if u.btw != nil {
		for _, image := range u.btw.pending.Images {
			used[image.Path] = true
		}
	}
	stacks := [][]composer.Undo{u.UndoDrafts, u.RedoDrafts}
	for _, call := range u.questions.calls {
		for _, question := range call.questions {
			e := question.editor
			stacks = append(stacks, e.UndoDrafts, e.RedoDrafts, []composer.Undo{e.Snapshot})
		}
	}
	if u.questions.active != nil {
		e := u.questions.parked
		stacks = append(stacks, e.UndoDrafts, e.RedoDrafts, []composer.Undo{e.Snapshot})
	}
	if u.approvals.open && u.approvals.pending[0].denialChoice() >= 0 {
		e := u.approvals.parked
		stacks = append(stacks, e.UndoDrafts, e.RedoDrafts, []composer.Undo{e.Snapshot})
	}
	for _, stack := range stacks {
		for _, snapshot := range stack {
			for _, draft := range []composer.Draft{snapshot.Draft, snapshot.HistoryDraft} {
				for _, image := range draft.Images {
					used[image.Path] = true
				}
			}
		}
	}
	for _, history := range [][]composer.Draft{u.InputHistory, {u.HistoryDraft}, draftValues(u.unsent), draftValues(u.queued)} {
		for _, snapshot := range history {
			for _, path := range snapshot.AnswerImages {
				used[path] = true
			}
			for _, image := range snapshot.Images {
				used[image.Path] = true
			}
		}
	}
	for path := range u.ownedImages {
		if !used[path] {
			_ = os.Remove(path)
			delete(u.ownedImages, path)
		}
	}
}

func (u *appServerUI) discardDraftImages() {
	for path := range u.ownedImages {
		_ = os.Remove(path)
	}
}
