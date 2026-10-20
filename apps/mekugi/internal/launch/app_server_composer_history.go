package launch

import (
	"os"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func rememberInput(conversation *conversationmodel.Session, draft conversationmodel.Input) {
	if draft.Question == nil && len(binding.QuestionReplies(draft.Text)) == 0 {
		conversation.Editor.Remember(draft.Draft)
	}
}
func draftSnapshot(editor *composer.Editor) conversationmodel.Input {
	return conversationmodel.Input{Draft: editor.Draft.Editable()}
}
func recordDraft(attached *attachedPresentation, editor *composer.Editor, terminal *terminalsession.Attachment, clock func() time.Time, owned map[string]bool, questions *nativeQuestionDock, approvals *nativeApprovalDock, side *sessioncontrol.Side) {
	editor.Record()
	setNotice(&attached.feedback, terminal, presentationTime(clock), "", false)
	pruneDraftImages(owned, editor, attached.conversation, questions, approvals, terminal.Prompts, side)
}

// Images referenced by undo/redo remain usable. Submission transfers file
// lifetime to Codex; only this session's never-submitted files are reclaimed.
func pruneDraftImages(owned map[string]bool, editor *composer.Editor, conversation *conversationmodel.Session, questions *nativeQuestionDock, approvals *nativeApprovalDock, prompts *terminalsession.Prompts, side *sessioncontrol.Side) {
	used := make(map[string]bool)
	for _, image := range editor.Images {
		used[image.Path] = true
	}
	if side != nil {
		for _, image := range side.Pending.Images {
			used[image.Path] = true
		}
	}
	stacks := [][]composer.Undo{editor.UndoDrafts, editor.RedoDrafts}
	for _, c := range questions.Calls {
		call := questions.handles[c]
		for _, e := range prompts.SecretState(call.ID).Editors {
			stacks = append(stacks, e.UndoDrafts, e.RedoDrafts, []composer.Undo{e.Snapshot})
		}
		for _, question := range call.Questions {
			e := question.Editor
			stacks = append(stacks, e.UndoDrafts, e.RedoDrafts, []composer.Undo{e.Snapshot})
		}
	}
	if questions.handles[questions.Active] != nil {
		e := conversation.PromptEditor.Parked
		stacks = append(stacks, e.UndoDrafts, e.RedoDrafts, []composer.Undo{e.Snapshot})
	}
	if approvals.Open && approvals.handles[approvals.Pending[0]].DenialChoice() >= 0 {
		e := conversation.PromptEditor.Parked
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
	for _, history := range [][]composer.Draft{conversation.Editor.InputHistory, {editor.HistoryDraft}, conversationmodel.Drafts(conversation.Unsent), conversationmodel.Drafts(conversation.Queued)} {
		for _, snapshot := range history {
			for _, path := range snapshot.AnswerImages {
				used[path] = true
			}
			for _, image := range snapshot.Images {
				used[image.Path] = true
			}
		}
	}
	for path := range owned {
		if !used[path] {
			_ = os.Remove(path)
			delete(owned, path)
		}
	}
}

func discardDraftImages(owned map[string]bool) {
	for path := range owned {
		_ = os.Remove(path)
	}
}
