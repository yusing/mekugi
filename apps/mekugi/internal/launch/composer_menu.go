package launch

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/completion"
)

func (p *composerPicker) openMenu(kind string) {
	p.cancelPermissionSelection()
	if !strings.HasPrefix(kind, "preset") {
		p.presets = nil
	}
	p.MenuState.Begin(sessionpresentation.MenuKind(kind))
	p.Open = true
}
func (p *composerPicker) menuView(workspace, text string, control *sessioncontrol.Live) *sessionpresentation.MenuView {
	if !p.Open {
		return nil
	}
	view := &sessionpresentation.MenuView{ID: p.ID, Kind: p.Kind, View: p.View(), Preferred: p.Preferred}
	if p.presets != nil {
		view.Title, view.Explanation = p.presets.Title(), p.presets.Explanation()
	}
	switch view.Kind {
	case sessionpresentation.MenuManageSkills:
		view.Saving = p.skillSaveOwnsManager(workspace)
	case sessionpresentation.MenuCompletion:
		view.QueueCompact = strings.TrimSpace(text) == "/compact"
	case sessionpresentation.MenuSettings:
		view.Setting = p.Setting
	case sessionpresentation.MenuSideReplace:
		view.Title = "Replace One-off?"
		view.Explanation = "Replace closes only One-off, interrupts its running work and creates a fresh snapshot of Main."
	case sessionpresentation.MenuArchive:
		view.Title = "Archive this session?"
		view.Explanation = archivePrompt(control)

	case sessionpresentation.MenuRewind:
		view.Dialog = true
		view.Title = "Rewind before a user turn"
		view.Explanation = "Rewind removes this turn and later conversation. Next, choose whether to keep or revert captured edits."
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
func (session *nativeSession) applyMenuResult(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, side **sessioncontrol.Side, clock func() time.Time, overlay bool, parent context.Context, result terminalsession.MenuResult) bool {
	if result.Action.Kind == sessionpresentation.MenuNoAction {
		return result.Handled
	}
	if picker.Kind == sessionpresentation.MenuSideReplace && result.Action.Kind == sessionpresentation.MenuAccept {
		if err := attached.presentation.AdmitMenu(result.Action); err != nil {
			setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
			return true
		}
		if result.Action.Choice.ID == 2 && (picker.sideTask == nil || side == nil || *side == nil || (*side).Key != picker.sideTarget) {
			setNotice(&attached.feedback, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return true
		}
		picker.Open, picker.Kind, picker.Choices = false, "", nil
		if result.Action.Choice.ID == 2 {
			pending := prepareDraft(picker, attached.cwd, session.skillEnvironment, sideTaskDraft(*picker.sideTask))
			pending.Origin = attached.view
			pending.Delivery = picker.sideTaskDelivery
			sourceEditor := *editor
			err := closeSide(attached, &session.Client, editor, &session.questions, &session.approvals, side, &session.ownedImages)
			if err == nil {
				sourceEditor.Take()
				err = openFreshSide(attached, &session.Client, editor, side, session.settings, presentationTime(clock), pending)
			}
			if err != nil {
				setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
			}
		}
		picker.sideTask = nil
		settlePresentation(attached, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, *side, clock, &session.questions, &session.approvals, overlay)
		return true
	}
	accepted := session.applyMenuAction(attached, picker, editor, *side, clock, overlay, parent, result.Action)
	picker.refreshPicker(attached, clock, session.Client.Requests, (*editor), parent, session.skillEnvironment)
	settlePresentation(attached, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, *side, clock, &session.questions, &session.approvals, overlay)
	return result.Handled && !(accepted && result.Submit)
}
func (session *nativeSession) applyMenuAction(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, side *sessioncontrol.Side, clock func() time.Time, overlay bool, parent context.Context, action sessionpresentation.MenuAction) bool {
	if err := attached.presentation.AdmitMenu(action); err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return false
	}
	p := picker
	if !p.Open || p.ID != action.Menu {
		setNotice(&attached.feedback, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
		return false
	}
	switch action.Kind {
	case sessionpresentation.MenuSearch:
		p.Query = action.Query
		if p.presets != nil {
			p.presets.Label = action.Query // Admitted only while typing a preset label.
		}
		return true
	case sessionpresentation.MenuDismiss:
		p.cancelPermissionSelection()
		p.presets = nil
		p.sideTask = nil
		switch p.Kind {
		case "menu", "manage":
			picker.closeSkillsModal(attached, clock, (*editor))
		default:
			p.Dismissed, p.Open = p.Target, false
			p.Kind, p.Choices, p.CopyTexts = "", nil, nil
			picker.Setting = ""
			p.rewindThread = ""
			picker.cancelScan()
		}
		return true
	case sessionpresentation.MenuAccept:
		index := slices.IndexFunc(p.Choices, func(c completion.Choice) bool { return c.Identity() == action.Choice })
		if index < 0 {
			setNotice(&attached.feedback, presentationTime(clock), sessionpresentation.ErrStaleIntent.Error(), true)
			return false
		}
		choice := p.Choices[index]
		if p.presets != nil {
			return choosePreset(attached, &session.Client, p, action, choice, session.restoring != nil)
		}
		switch p.Kind {
		case "menu":
			picker.chooseSkillsAction(attached, (*editor), clock, session.ownedImages, &session.questions, &session.approvals, side, choice)
		case "manage":
			toggleSkill(picker, session.Client.Requests, attached.cwd, choice)
		case "rewind", "rewind-edits":
			chooseRewind(attached, &session.Client, picker, session.restoring != nil, clock, choice, parent)
		case "archive":
			chooseArchive(picker, &session.Client, &attached.feedback, clock, choice.ID == archiveConfirmChoice)
		case "settings":
			chooseSetting(attached, &session.Client, session.settings, &session.serviceTiers, session.panes, session.restoring != nil, clock, picker, choice)
		case "copy":
			if attached.effects != nil {
				attached.effects.add(sessionapi.NativeEffect{Thread: session.Client.Thread, Clipboard: new(p.CopyTexts[index])})
			} else {
				setNotice(&attached.feedback, presentationTime(clock), "Terminal clipboard unavailable", true)
			}
			p.Kind, p.Open, p.Choices, p.CopyTexts = "", false, nil, nil
		default:
			return session.acceptCompletion(attached, picker, editor, side, clock, overlay, parent, choice)
		}
		return true
	}
	return false
}
