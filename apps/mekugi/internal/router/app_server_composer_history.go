package router

import (
	"os"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func (u *appServerUI) rememberInput(draft conversationmodel.Input) {
	if draft.Question == nil && len(binding.QuestionReplies(draft.Text)) == 0 {
		u.conversation().Editor.Remember(draft.Draft)
	}
}
func (u *appServerUI) recallInput(backward bool) {
	if u.Editor.Recall(backward) {
		u.mainView.CursorColumn = nil
		u.setNotice("", false)
		u.pruneDraftImages()
	}
}
func (u *appServerUI) draftSnapshot() conversationmodel.Input {
	return conversationmodel.Input{Draft: u.Draft.Editable()}
}
func (u *appServerUI) loadDraft(d conversationmodel.Input) { u.Editor.Replace(d.Draft) }
func (u *appServerUI) recordDraft() {
	u.Editor.Record()
	u.setNotice("", false)
	u.pruneDraftImages()
}
func (u *appServerUI) undoDraft(redo bool) {
	u.Editor.Undo(redo)
	u.mainView.CursorColumn = nil
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
		for _, image := range u.btw.Pending.Images {
			used[image.Path] = true
		}
	}
	stacks := [][]composer.Undo{u.UndoDrafts, u.RedoDrafts}
	for _, call := range u.questionCalls() {
		for _, e := range call.secrets {
			stacks = append(stacks, e.UndoDrafts, e.RedoDrafts, []composer.Undo{e.Snapshot})
		}
		for _, question := range call.Questions {
			e := question.Editor
			stacks = append(stacks, e.UndoDrafts, e.RedoDrafts, []composer.Undo{e.Snapshot})
		}
	}
	if u.activeQuestionCall() != nil {
		e := u.promptEditor.Parked
		stacks = append(stacks, e.UndoDrafts, e.RedoDrafts, []composer.Undo{e.Snapshot})
	}
	if u.approvals.Open && u.pendingApprovals()[0].DenialChoice() >= 0 {
		e := u.promptEditor.Parked
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
	for _, history := range [][]composer.Draft{u.conversation().Editor.InputHistory, {u.HistoryDraft}, conversationmodel.Drafts(u.conversation().Unsent), conversationmodel.Drafts(u.conversation().Queued)} {
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
