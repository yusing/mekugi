package router

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/completion"
)

func (p *composerPicker) openMenu(kind string) {
	p.MenuState.Begin(sessionpresentation.MenuKind(kind))
	p.Open = true
}
func (p *composerPicker) menuView(workspace, text string, control *sessioncontrol.Live) *sessionpresentation.MenuView {
	if !p.Open {
		return nil
	}
	view := &sessionpresentation.MenuView{ID: p.ID, Kind: p.Kind, View: p.View(), Preferred: p.Preferred}
	switch view.Kind {
	case sessionpresentation.MenuManageSkills:
		view.Saving = p.skillSaveOwnsManager(workspace)
	case sessionpresentation.MenuCompletion:
		view.QueueCompact = strings.TrimSpace(text) == "/compact"
	case sessionpresentation.MenuSettings:
		view.Setting = p.Setting
	case sessionpresentation.MenuArchive:
		view.Explanation = archivePrompt(control)
	}
	return view
}

// Apply each menu effect and publish before a continuation can derive another intent.
func (u *appServerUI) applyMenuResult(result terminalsession.MenuResult) bool {
	if result.Action.Kind == sessionpresentation.MenuNoAction {
		return result.Handled
	}
	accepted := u.applyMenuAction(result.Action)
	u.picker.refreshPicker(&u.attached, u.shell, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
	u.settlePresentation()
	return result.Handled && !(accepted && result.Submit)
}
func (u *appServerUI) applyMenuAction(action sessionpresentation.MenuAction) bool {
	if err := u.attached.presentation.AdmitMenu(action); err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return false
	}
	p := &u.picker
	if !p.Open || p.ID != action.Menu {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	switch action.Kind {
	case sessionpresentation.MenuSearch:
		p.Query = action.Query
		return true
	case sessionpresentation.MenuDismiss:
		switch p.Kind {
		case "menu", "manage":
			u.picker.closeSkillsModal(&u.attached, u.shell, u.clock, u.Editor)
		default:
			p.Dismissed, p.Open = p.Target, false
			p.Kind, p.Choices, p.CopyTexts = "", nil, nil
			u.picker.Setting = ""
			u.picker.cancelScan()
		}
		return true
	case sessionpresentation.MenuAccept:
		index := slices.IndexFunc(p.Choices, func(c completion.Choice) bool { return c.Identity() == action.Choice })
		if index < 0 {
			setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false
		}
		choice := p.Choices[index]
		switch p.Kind {
		case "menu":
			u.picker.chooseSkillsAction(&u.attached, u.Editor, u.shell, u.clock, u.ownedImages, &u.questions, &u.approvals, u.side, choice)
		case "manage":
			toggleSkill(&u.picker, u.Requests, u.attached.cwd, choice)
		case "archive":
			u.chooseArchive(choice.ID == archiveConfirmChoice)
		case "settings":
			u.chooseSetting(choice)
		case "copy":
			if u.shell == nil {
				setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Terminal clipboard unavailable", true)
			} else {
				u.copyText(u.shell, p.CopyTexts[index])
			}
			p.Kind, p.Open, p.Choices, p.CopyTexts = "", false, nil, nil
		default:
			return u.acceptCompletion(choice)
		}
		return true
	}
	return false
}
