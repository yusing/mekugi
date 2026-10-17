package router

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/completion"
)

func (u *appServerUI) openMenu(kind string) {
	u.menuSequence++
	p := &u.picker
	p.id, p.modal, p.Open, p.preferred = u.menuSequence, kind, true, completion.ChoiceID{}
}
func (u *appServerUI) menuView() *sessionpresentation.MenuView {
	p := &u.picker
	if !p.Open {
		return nil
	}
	view := &sessionpresentation.MenuView{ID: p.id, Kind: sessionpresentation.MenuKind(p.modal), View: p.View(), Preferred: p.preferred}
	switch view.Kind {
	case sessionpresentation.MenuManageSkills:
		view.Saving = u.skillSaveOwnsManager()
	case sessionpresentation.MenuCompletion:
		view.QueueCompact = strings.TrimSpace(u.Text) == "/compact"
	case sessionpresentation.MenuSettings:
		view.Setting = u.settingsChoices
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
	if err := u.sessionPresentation().AdmitMenu(action); err != nil {
		u.setNotice(err.Error(), true)
		return false
	}
	p := &u.picker
	if !p.Open || p.id != action.Menu {
		u.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	switch action.Kind {
	case sessionpresentation.MenuSearch:
		p.Query = action.Query
		return true
	case sessionpresentation.MenuDismiss:
		switch p.modal {
		case "menu", "manage":
			u.closeSkillsModal()
		default:
			p.Dismissed, p.Open = p.Target, false
			p.modal, p.Choices, p.copyTexts = "", nil, nil
			u.settingsChoices = ""
			u.cancelPickerScan()
		}
		return true
	case sessionpresentation.MenuAccept:
		index := slices.IndexFunc(p.Choices, func(c completion.Choice) bool { return c.Identity() == action.Choice })
		if index < 0 {
			u.setNotice(sessionpresentation.ErrStaleIntent.Error(), true)
			return false
		}
		choice := p.Choices[index]
		switch p.modal {
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
				u.copyText(u.shell, p.copyTexts[index])
			}
			p.modal, p.Open, p.Choices, p.copyTexts = "", false, nil, nil
		default:
			return u.acceptCompletion(choice)
		}
		return true
	}
	return false
}
