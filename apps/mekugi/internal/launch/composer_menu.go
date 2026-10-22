package launch

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

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
	case sessionpresentation.MenuSideReplace:
		view.Title = "Replace Side?"
		if p.sideTask != nil {
			view.Title = "Replace with One-off?"
		}
		view.Explanation = "Replace closes only Side, interrupts its running answer and creates a fresh snapshot of Main."
	case sessionpresentation.MenuArchive:
		view.Title = "Archive this session?"
		view.Explanation = archivePrompt(control)
		if p.archiveTarget != "" {
			view.Dialog = true
			view.Title = "Archive the original session?"
			view.Explanation = "The new branch is ready. Archive original thread " + p.archiveTarget + "?"
		}
	case sessionpresentation.MenuRewind:
		view.Dialog = true
		view.Title = "Rewind before a user turn"
		view.Explanation = "The branch excludes this turn and later conversation. Next, choose whether to keep or revert captured edits."
	case sessionpresentation.MenuRewindEdits:
		view.Dialog = true
		view.Title = "Keep or revert edits?"
		view.Explanation = "Keep files, or revert captured edits from this turn onward, including descendants. Conflicts stop the rewind."
		if p.rewind != nil && p.Loading {
			view.Choices = nil
			view.Title = "Checking captured edits…"
			view.Explanation = "Rewind continues automatically if there are no captured edits to revert."
		}
		if p.rewind != nil && p.rewind.problem != "" {
			view.Explanation = "Revert unavailable: " + p.rewind.problem + ". Keep edits leaves files unchanged."
		}
	}
	return view
}

// Apply each menu effect and publish before a continuation can derive another intent.
func applyMenuResult(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], applied preferences.Applied, tiers **inferencegateway.Tiers, title string, backend string, restoring bool, side **sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, owned map[string]bool, panes *nativePreferences, parent context.Context, environment []string, result terminalsession.MenuResult) bool {
	if result.Action.Kind == sessionpresentation.MenuNoAction {
		return result.Handled
	}
	if picker.Kind == sessionpresentation.MenuSideReplace && result.Action.Kind == sessionpresentation.MenuAccept {
		if err := attached.presentation.AdmitMenu(result.Action); err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
			return true
		}
		if result.Action.Choice.ID == 2 && (side == nil || *side == nil || (*side).Key != picker.sideTarget) {
			setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return true
		}
		picker.Open, picker.Kind, picker.Choices = false, "", nil
		if result.Action.Choice.ID == 2 {
			pending := conversationmodel.Input{}
			oneoff := picker.sideTask != nil
			sourceEditor := *editor
			if oneoff {
				pending = prepareDraft(picker, terminal, attached.cwd, environment, sideTaskDraft(*picker.sideTask))
				pending.Origin = attached.view
				pending.Delivery = picker.sideTaskDelivery
			}
			err := closeSide(attached, terminal, control, editor, questions, approvals, side, &owned)
			if err == nil {
				if oneoff {
					sourceEditor.Take()
				}
				err = openFreshSide(attached, terminal, control, editor, side, applied, presentationTime(clock), oneoff, pending)
			}
			if err != nil {
				setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
			}
		}
		picker.sideTask = nil
		settlePresentation(attached, terminal, control, picker, editor, childKey, applied, *tiers, title, backend, restoring, *side, clock, questions, approvals, overlay)
		return true
	}
	accepted := applyMenuAction(attached, terminal, control, picker, editor, childKey, applied, tiers, title, backend, restoring, *side, clock, questions, approvals, overlay, owned, panes, parent, environment, result.Action)
	picker.refreshPicker(attached, terminal, clock, control.Requests, (*editor), parent, environment)
	settlePresentation(attached, terminal, control, picker, editor, childKey, applied, (*tiers), title, backend, restoring, *side, clock, questions, approvals, overlay)
	return result.Handled && !(accepted && result.Submit)
}
func applyMenuAction(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], applied preferences.Applied, tiers **inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, owned map[string]bool, panes *nativePreferences, parent context.Context, environment []string, action sessionpresentation.MenuAction) bool {
	if err := attached.presentation.AdmitMenu(action); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return false
	}
	p := picker
	if !p.Open || p.ID != action.Menu {
		setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	switch action.Kind {
	case sessionpresentation.MenuSearch:
		p.Query = action.Query
		return true
	case sessionpresentation.MenuDismiss:
		p.sideTask = nil
		switch p.Kind {
		case "menu", "manage":
			picker.closeSkillsModal(attached, terminal, clock, (*editor))
		default:
			p.Dismissed, p.Open = p.Target, false
			p.Kind, p.Choices, p.CopyTexts = "", nil, nil
			picker.Setting = ""
			p.archiveTarget, p.rewindThread = "", ""
			picker.cancelScan()
		}
		return true
	case sessionpresentation.MenuAccept:
		index := slices.IndexFunc(p.Choices, func(c completion.Choice) bool { return c.Identity() == action.Choice })
		if index < 0 {
			setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false
		}
		choice := p.Choices[index]
		switch p.Kind {
		case "menu":
			picker.chooseSkillsAction(attached, (*editor), terminal, clock, owned, questions, approvals, side, choice)
		case "manage":
			toggleSkill(picker, control.Requests, attached.cwd, choice)
		case "rewind", "rewind-edits":
			chooseRewind(attached, terminal, control, picker, applied, restoring, clock, choice, parent)
		case "archive":
			chooseArchive(picker, control, &attached.feedback, terminal, clock, choice.ID == archiveConfirmChoice)
		case "settings":
			chooseSetting(attached, terminal, control, applied, tiers, panes, restoring, clock, picker, choice)
		case "copy":
			if attached.effects != nil {
				attached.effects.add(sessionapi.NativeEffect{Thread: control.Thread, Clipboard: new(p.CopyTexts[index])})
			} else if terminal == nil {
				setNotice(&attached.feedback, terminal, presentationTime(clock), "Terminal clipboard unavailable", true)
			} else {
				copyText(&attached.feedback, terminal, clock, terminal, p.CopyTexts[index])
			}
			p.Kind, p.Open, p.Choices, p.CopyTexts = "", false, nil, nil
		default:
			return acceptCompletion(attached, terminal, control, picker, editor, childKey, applied, (*tiers), title, backend, restoring, side, clock, questions, approvals, overlay, owned, parent, environment, choice)
		}
		return true
	}
	return false
}
