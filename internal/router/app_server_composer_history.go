package router

import (
	"os"
	"slices"
)

type composerDraft struct {
	questionCall     *nativeQuestionCall
	text             string
	cursorBack       int
	images           []composerImage
	skills           []composerSkill
	files            []composerFile
	attachments      []string // Immutable snapshots captured when queued/submitted.
	attachmentNotice string
	inHistory        bool // An accepted steer being resent is already in input history.
}

// History is local to this thread. Resume hydrates text from Codex history;
// session entries retain attachments without creating a second durable store.
func (u *appServerUI) rememberInput(draft composerDraft) {
	if draft.text == "" || draft.questionCall != nil || len(questionReplies(draft.text)) > 0 {
		return
	}
	u.inputHistory = append(u.inputHistory, draft)
	if len(u.inputHistory) > 100 {
		u.inputHistory = slices.Clone(u.inputHistory[len(u.inputHistory)-100:])
	}
}

func (u *appServerUI) recallInput(backward bool) {
	next := u.historyBack - 1
	if backward {
		next = u.historyBack + 1
	}
	if next < 0 || next > len(u.inputHistory) {
		return
	}
	if u.historyBack == 0 {
		u.historyDraft = u.draftSnapshot()
	}
	u.recordDraft()
	u.historyBack = next
	snapshot := u.historyDraft
	if next > 0 {
		snapshot = u.inputHistory[len(u.inputHistory)-next]
	}
	u.draft, u.cursorBack, u.images = snapshot.text, snapshot.cursorBack, slices.Clone(snapshot.images)
	u.skills = slices.Clone(snapshot.skills)
	u.files = slices.Clone(snapshot.files)
	u.cursorColumn = nil
}

func (u *appServerUI) draftSnapshot() composerDraft {
	return composerDraft{text: u.draft, cursorBack: u.cursorBack, images: slices.Clone(u.images), skills: slices.Clone(u.skills), files: slices.Clone(u.files)}
}

func (u *appServerUI) loadDraft(d composerDraft) {
	u.draft, u.cursorBack, u.images = d.text, d.cursorBack, slices.Clone(d.images)
	u.skills, u.files = slices.Clone(d.skills), slices.Clone(d.files)
}

type composerUndo struct {
	composerDraft
	historyBack  int
	historyDraft composerDraft
}

func (u *appServerUI) undoSnapshot() composerUndo {
	return composerUndo{u.draftSnapshot(), u.historyBack, u.historyDraft}
}

func (u *appServerUI) recordDraft() {
	u.undoDrafts = append(u.undoDrafts, u.undoSnapshot())
	if len(u.undoDrafts) > 100 {
		u.undoDrafts = slices.Clone(u.undoDrafts[len(u.undoDrafts)-100:])
	}
	u.redoDrafts = nil
	u.run = runNone
	u.setNotice("", false)
	u.pruneDraftImages()
}

func (u *appServerUI) undoDraft(redo bool) {
	from, to := &u.undoDrafts, &u.redoDrafts
	if redo {
		from, to = to, from
	}
	u.run = runNone
	u.cursorColumn = nil
	u.setNotice("", false)
	if len(*from) == 0 {
		return
	}
	*to = append(*to, u.undoSnapshot())
	snapshot := (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	u.historyBack, u.historyDraft = snapshot.historyBack, snapshot.historyDraft
	u.draft, u.cursorBack, u.images = snapshot.text, snapshot.cursorBack, slices.Clone(snapshot.images)
	u.skills = slices.Clone(snapshot.skills)
	u.files = slices.Clone(snapshot.files)
}

// Images referenced by undo/redo remain usable. Submission transfers file
// lifetime to Codex; only this session's never-submitted files are reclaimed.
func (u *appServerUI) pruneDraftImages() {
	used := make(map[string]bool)
	for _, image := range u.images {
		used[image.path] = true
	}
	stacks := [][]composerUndo{u.undoDrafts, u.redoDrafts}
	if u.questions.active != nil {
		e := u.questions.parked
		stacks = append(stacks, e.undo, e.redo, []composerUndo{e.snapshot})
	}
	for _, stack := range stacks {
		for _, snapshot := range stack {
			for _, draft := range []composerDraft{snapshot.composerDraft, snapshot.historyDraft} {
				for _, image := range draft.images {
					used[image.path] = true
				}
			}
		}
	}
	for _, history := range [][]composerDraft{u.inputHistory, {u.historyDraft}, u.unsent, u.queued} {
		for _, snapshot := range history {
			for _, image := range snapshot.images {
				used[image.path] = true
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
