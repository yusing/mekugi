package router

import (
	"maps"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

// Click-through previews retain presentation state only, never captured data or
// host lifecycle state. New activity and captures remain available on return.
type nativeNavigationReturn struct {
	side, activityOpen, diffOpen bool
	focus                        int
	main, agents                 nativeTranscriptReturn
	diff                         nativeDiffReturn
}
type nativeTranscriptReturn struct {
	offset          int
	following, only bool
	selected        string
}
type nativeDiffReturn struct {
	navigation       diffview.Navigation
	selected, caller string
	scroll           map[string]int
	following, mode  bool
	back             liveDiffBack
}

func transcriptReturn(v *liveActivityView) nativeTranscriptReturn {
	return nativeTranscriptReturn{v.offset, v.following, v.only, v.selected}
}
func (b nativeTranscriptReturn) restore(v *liveActivityView) {
	v.offset, v.following, v.only, v.selected = b.offset, b.following, b.only, b.selected
	v.pendingTarget, v.flashQuestion = 0, 0
	v.runs = nil
}
func (u *terminalUI) navigationReturn() nativeNavigationReturn {
	b := nativeNavigationReturn{side: u.side, activityOpen: u.activityOpen, diffOpen: u.diffOpen, focus: u.focus, main: transcriptReturn(u.main.view), agents: transcriptReturn(u.agents)}
	c := u.diff
	b.diff = nativeDiffReturn{navigation: c.navigation, caller: c.view.Caller, scroll: maps.Clone(c.view.Scroll), following: c.view.Following, mode: c.diffMode, back: c.back}
	b.diff.navigation.Collapsed = maps.Clone(c.navigation.Collapsed)
	b.diff.navigation.Changes.Expanded = maps.Clone(c.navigation.Changes.Expanded)
	if c.view.Selected >= 0 && c.view.Selected < len(c.view.Files) {
		b.diff.selected = c.view.Files[c.view.Selected].Key()
	}
	return b
}
func (u *terminalUI) pushNavigationReturn() {
	u.navigationReturns = append(u.navigationReturns, u.navigationReturn())
}
func (u *terminalUI) popNavigationReturn() bool {
	if len(u.navigationReturns) == 0 || u.main.paste {
		return false
	}
	c := u.diff
	if u.focus == 1 && (c.help || c.navigation.Filtering || c.back.kind != 0) {
		return false
	}
	index := len(u.navigationReturns) - 1
	b := u.navigationReturns[index]
	u.navigationReturns[index] = nativeNavigationReturn{}
	u.navigationReturns = u.navigationReturns[:index]
	u.restoreNavigationReturn(b)
	return true
}

func (u *terminalUI) restoreNavigationReturn(b nativeNavigationReturn) {
	c := u.diff
	u.side, u.activityOpen, u.diffOpen, u.focus = b.side, b.activityOpen, b.diffOpen, b.focus
	b.main.restore(u.main.view)
	b.agents.restore(u.agents)
	c.filterCaller(b.diff.caller)
	c.navigation = b.diff.navigation
	for i, file := range c.view.Files {
		if file.Key() == b.diff.selected {
			c.view.Selected = i
			break
		}
	}
	c.view.Scroll, c.view.Following, c.diffMode, c.back = b.diff.scroll, b.diff.following, b.diff.mode, b.diff.back
	c.files = make([]livediff.File, len(c.view.Files))
	for i, file := range c.view.Files {
		c.files[i] = c.view.Visible[file.Key()]
	}
	c.navigation.Rebuild(c.files, c.workspace)
	c.refreshChanges()
	c.dirty = true
}
