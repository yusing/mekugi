package router

import "strings"

// Docks and answer editors stay with their execution owner. The viewed shell
// borrows only that editor, never its transcript or ordinary input lifecycle.
func (u *appServerUI) promptViews() []*appServerUI {
	if n := u.navigation; n != nil {
		views := make([]*appServerUI, 0, len(n.order))
		for _, thread := range n.order {
			views = append(views, n.views[thread])
		}
		return views
	}
	return []*appServerUI{u}
}

func (u *appServerUI) promptEditor() *appServerUI {
	for _, v := range u.promptViews() {
		if v.approvals.open || v.questions.active != nil {
			return v
		}
	}
	return u
}

func (u *appServerUI) nextPromptOrder() uint64 {
	if n := u.navigation; n != nil {
		n.promptOrder++
		return n.promptOrder
	}
	return 0
}

func (u *appServerUI) approvalSource() *appServerUI {
	var source *appServerUI
	for _, v := range u.promptViews() {
		if len(v.approvals.pending) == 0 {
			continue
		}
		if v.approvals.open {
			return v
		}
		if source == nil || v.approvals.pending[0].order < source.approvals.pending[0].order {
			source = v
		}
	}
	return source
}

func (u *appServerUI) questionSource() *appServerUI {
	var source *appServerUI
	var first *nativeQuestionCall
	for _, v := range u.promptViews() {
		if v.questions.active != nil {
			return v
		}
		for _, c := range v.questions.calls {
			if c.resolved || c.sent {
				continue
			}
			sync := len(c.request) > 0
			if first == nil || sync && len(first.request) == 0 || sync == (len(first.request) > 0) && c.order < first.order {
				source, first = v, c
			}
		}
	}
	return source
}

func (u *appServerUI) hidePromptEditors() {
	for _, v := range u.promptViews() {
		v.hideQuestions()
		v.hideApprovals()
	}
}

func (u *appServerUI) focusPrompt() {
	viewed := u.viewedUI()
	viewed.picker.open, viewed.keybindings = false, false
	viewed.cancelPickerScan()
	if viewed.shell != nil {
		viewed.shell.focus = 0
		viewed.shell.selection = nil
	}
	viewed.dirty = true
}

func (u *appServerUI) promptLabel(thread string) string {
	if u.navigation == nil {
		return ""
	}
	label := u.orchestrationTitle()
	if path := u.session.paths[thread]; path != "" && path != "/root" {
		label += " / " + strings.TrimPrefix(path, "/root/")
	}
	return label
}
