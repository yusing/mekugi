package router

import (
	"os"
	"slices"
)

type composerDraft struct {
	text       string
	cursorBack int
	images     []composerImage
}

func (u *appServerUI) draftSnapshot() composerDraft {
	return composerDraft{u.draft, u.cursorBack, slices.Clone(u.images)}
}

func (u *appServerUI) recordDraft() {
	u.undoDrafts = append(u.undoDrafts, u.draftSnapshot())
	if len(u.undoDrafts) > 100 {
		u.undoDrafts = slices.Clone(u.undoDrafts[len(u.undoDrafts)-100:])
	}
	u.redoDrafts = nil
	u.run = runNone
	u.notice, u.noticeAlert = "", false
	u.pruneDraftImages()
}

func (u *appServerUI) undoDraft(redo bool) {
	from, to := &u.undoDrafts, &u.redoDrafts
	if redo {
		from, to = to, from
	}
	u.run = runNone
	u.cursorColumn = nil
	u.notice, u.noticeAlert = "", false
	if len(*from) == 0 {
		return
	}
	*to = append(*to, u.draftSnapshot())
	snapshot := (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	u.draft, u.cursorBack, u.images = snapshot.text, snapshot.cursorBack, slices.Clone(snapshot.images)
}

// Images referenced by undo/redo remain usable. Submission transfers file
// lifetime to Codex; only this session's never-submitted files are reclaimed.
func (u *appServerUI) pruneDraftImages() {
	used := make(map[string]bool)
	for _, image := range u.images {
		used[image.path] = true
	}
	for _, history := range [][]composerDraft{u.undoDrafts, u.redoDrafts} {
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
