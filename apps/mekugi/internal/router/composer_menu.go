package router

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/completion"
)

func (u *appServerUI) openMenu(kind string) {
	p := &u.picker
	p.MenuState.Begin(sessionpresentation.MenuKind(kind))
	p.Open = true
}
func (u *appServerUI) menuView() *sessionpresentation.MenuView {
	p := &u.picker
	if !p.Open {
		return nil
	}
	view := &sessionpresentation.MenuView{ID: p.ID, Kind: p.Kind, View: p.View(), Preferred: p.Preferred}
	switch view.Kind {
	case sessionpresentation.MenuManageSkills:
		view.Saving = u.skillSaveOwnsManager()
	case sessionpresentation.MenuCompletion:
		view.QueueCompact = strings.TrimSpace(u.Text) == "/compact"
	case sessionpresentation.MenuSettings:
		view.Setting = u.picker.Setting
	case sessionpresentation.MenuArchive:
		view.Explanation = u.archivePrompt()
	}
	return view
}

// Apply each menu effect and publish before a continuation can derive another intent.
func (u *appServerUI) applyMenuResult(result terminalsession.MenuResult) bool {
	if result.Action.Kind == sessionpresentation.MenuNoAction {
		return result.Handled
	}
	accepted := u.applyMenuAction(result.Action)
	u.refreshPicker()
	u.settlePresentation()
	return result.Handled && !(accepted && result.Submit)
}
func (u *appServerUI) applyMenuAction(action sessionpresentation.MenuAction) bool {
	if err := u.attached.presentation.AdmitMenu(action); err != nil {
		u.setNotice(err.Error(), true)
		return false
	}
	p := &u.picker
	if !p.Open || p.ID != action.Menu {
		u.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	switch action.Kind {
	case sessionpresentation.MenuSearch:
		p.Query = action.Query
		return true
	case sessionpresentation.MenuDismiss:
		switch p.Kind {
		case "menu", "manage":
			u.closeSkillsModal()
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
			u.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
			return false
		}
		choice := p.Choices[index]
		switch p.Kind {
		case "menu":
			u.chooseSkillsAction(choice)
		case "manage":
			u.toggleSkill(choice)
		case "archive":
			u.chooseArchive(choice.ID == archiveConfirmChoice)
		case "settings":
			u.chooseSetting(choice)
		case "copy":
			if u.shell == nil {
				u.setNotice("Terminal clipboard unavailable", true)
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
