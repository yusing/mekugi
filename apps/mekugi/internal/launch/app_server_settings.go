package launch

import (
	"encoding/json/jsontext"
	"time"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/modules/conversation/completion"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

type appServerModel struct {
	Model         string `json:"model"`
	Hidden        bool   `json:"hidden"`
	DefaultEffort string `json:"defaultReasoningEffort"`
	Efforts       []struct {
		Effort string `json:"reasoningEffort"`
	} `json:"supportedReasoningEfforts"`
	Tiers []struct {
		ID string `json:"id"`
	} `json:"serviceTiers"`
}

func currentModel(models []appServerModel, model string) *appServerModel {
	for i := range models {
		if models[i].Model == model {
			return &models[i]
		}
	}
	return nil
}

func effortChoices(models []appServerModel, selected string) []string {
	var choices []string
	if model := currentModel(models, selected); model != nil {
		for _, effort := range model.Efforts {
			choices = append(choices, effort.Effort)
		}
	}
	return choices
}

func selectedSettings(attached *attachedPresentation, requests *sessioncontrol.Requests[appServerRequest], main preferences.Applied) (*sessioncontrol.Side, preferences.Applied) {
	if attached.presentation.DisplayingSide() {
		if side := requests.Side(attached.presentation.View().SideThread.ID); side != nil {
			return side, preferences.Applied{Model: side.Model, Effort: side.Effort, Tier: side.Tier}
		}
	}
	return nil, main
}

func settingsChoices(command string, models []appServerModel, applied preferences.Applied, tiers *inferencegateway.Tiers, thread string) ([]string, string) {
	var choices []string
	current := applied.Model
	switch command {
	case "/model":
		for _, model := range models {
			if !model.Hidden {
				choices = append(choices, model.Model)
			}
		}
	case "/reasoning", "/effort":
		current, choices = applied.Effort, effortChoices(models, applied.Model)
	case "/tier":
		current, choices = tiers.Effective(applied.Model, applied.Tier, thread), []string{"default"}
		if model := currentModel(models, applied.Model); model != nil {
			for _, tier := range model.Tiers {
				choices = append(choices, tier.ID)
			}
		}
	}
	return choices, current
}

func refreshSettingsPicker(p *composerPicker, thread string, models []appServerModel, applied preferences.Applied, tiers *inferencegateway.Tiers, problem string) {
	if p.Kind != "settings" || p.Setting == "/permissions" {
		return
	}
	if p.settingThread != thread {
		problem = "Settings target changed · reopen the picker"
	}
	if problem != "" {
		p.Loading, p.Problem = false, problem
		return
	}
	choices, current := settingsChoices(p.Setting, models, applied, tiers, thread)
	p.showSettingsPicker(false, p.Setting, choices, current)
}

// Codex owns configuration_update history items. These RPCs submit settings
// intent, never synthetic Responses items or writes to the user's config file.
// Source: codex-rs/app-server-protocol/src/protocol/v2/{thread,turn}.rs
func updateSettings(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], applied preferences.Applied, tiers **inferencegateway.Tiers, panes *nativePreferences, restoring bool, clock func() time.Time, change map[string]any) (bool, error) {
	return updateSettingsForTurn(attached, control, applied, tiers, panes, restoring, clock, change, nil)
}

func updateSettingsForTurn(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], applied preferences.Applied, tiers **inferencegateway.Tiers, panes *nativePreferences, restoring bool, clock func() time.Time, change map[string]any, originalTurn *string) (bool, error) {
	decision := control.PrepareSettings(applied, change, restoring)
	side, applied := selectedSettings(attached, control.Requests, applied)
	if side != nil {
		decision = side.PrepareSettings(change)
	}
	if attached.presentation.DisplayingSide() && side == nil || restoring || control.Replacement.Pending() {
		decision.Action = sessioncontrol.SettingsUnavailable
	}
	if originalTurn != nil && decision.Turn != *originalTurn {
		decision.Turn = ""
		if decision.Action == sessioncontrol.SettingsTurn {
			decision.Action = sessioncontrol.SettingsUnchanged
		}
	}
	switch decision.Action {
	case sessioncontrol.SettingsUnavailable:
		setNotice(&attached.feedback, presentationTime(clock), "Wait for the thread or turn to finish starting", true)
		return false, nil
	case sessioncontrol.SettingsAlreadyPending:
		setNotice(&attached.feedback, presentationTime(clock), "Settings update pending", false)
		return false, nil
	}
	if side != nil {
		if err := control.Requests.ApplySideSettings(side, decision); err != nil {
			return false, err
		}
	} else {
		if err := control.Requests.ApplySettings(&control.Live, decision); err != nil {
			return false, err
		}
	}

	if decision.RestoreDefault {
		paneError(attached.source, panes.retainAppliedSettings(preferences.Scope{Workspace: attached.cwd, Thread: control.Thread}, control.Settings.RestoreEffort, preferences.Applied{Model: applied.Model, Effort: applied.Effort, Tier: applied.Tier, Observed: presentationTime(clock)}))
	}
	if decision.ChooseTier {
		(*tiers) = chooseServiceTier((*tiers), applied.Model, control.Thread, applied.Tier)
	}
	switch decision.Action {
	case sessioncontrol.SettingsUnchanged:
		setNotice(&attached.feedback, presentationTime(clock), "Settings unchanged", false)
		if decision.ChooseTier {
			setNotice(&attached.feedback, presentationTime(clock), "Service tier updated", false)
		}
	case sessioncontrol.SettingsTurn:
		setNotice(&attached.feedback, presentationTime(clock), "Updating active turn…", false)
	case sessioncontrol.SettingsThread:
		setNotice(&attached.feedback, presentationTime(clock), "Updating settings…", false)
	}
	return true, nil
}

type pendingReasoning struct {
	attached     *attachedPresentation
	thread, turn string
	up           bool
}

func stepReasoning(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], applied preferences.Applied, tiers **inferencegateway.Tiers, panes *nativePreferences, restoring bool, clock func() time.Time, models []appServerModel, loading bool, up bool) error {
	side, applied := selectedSettings(attached, control.Requests, applied)
	if attached.presentation.DisplayingSide() && side == nil {
		setNotice(&attached.feedback, presentationTime(clock), "Wait for the side conversation to finish starting", false)
		return nil
	}
	if loading {
		turn := control.Turn
		if side != nil {
			turn = side.Turn
		}
		attached.reasoning = &pendingReasoning{attached: attached, thread: attached.presentation.View().Input.ID, turn: turn, up: up}
		setNotice(&attached.feedback, presentationTime(clock), "Loading model choices…", false)
		return nil
	}
	return stepReasoningForTurn(attached, control, applied, tiers, panes, restoring, clock, models, up, nil)
}

func stepReasoningForTurn(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], applied preferences.Applied, tiers **inferencegateway.Tiers, panes *nativePreferences, restoring bool, clock func() time.Time, models []appServerModel, up bool, originalTurn *string) error {
	_, applied = selectedSettings(attached, control.Requests, applied)
	choices := effortChoices(models, applied.Model)
	if len(choices) == 0 {
		setNotice(&attached.feedback, presentationTime(clock), "Reasoning choices unavailable · use /reasoning VALUE", true)
		return nil
	}
	index := slices.Index(choices, applied.Effort)
	if index < 0 {
		index = slices.Index(choices, currentModel(models, applied.Model).DefaultEffort)
	}
	if index < 0 {
		index = 0
	}
	next := index - 1
	if up {
		next = index + 1
	}
	if next < 0 || next >= len(choices) {
		return nil
	}
	_, err := updateSettingsForTurn(attached, control, applied, tiers, panes, restoring, clock, map[string]any{"effort": choices[next]}, originalTurn)
	return err
}

// Slash controls work while a turn runs and do not submit conversation input.
func (session *nativeSession) settingsCommand(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, side *sessioncontrol.Side, clock func() time.Time, overlay bool, text string) (bool, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false, nil
	}
	command := fields[0]
	if command != "/model" && command != "/reasoning" && command != "/effort" && command != "/tier" && command != "/permissions" && command != "/live" {
		return false, nil
	}
	if session.Client.Replacement.Pending() {
		setNotice(&attached.feedback, presentationTime(clock), "Wait for the new session · draft kept", false)
		return true, nil
	}
	if command == "/live" {
		value := "off"
		if attached.liveHidden {
			value = "on"
		}
		if len(fields) == 2 {
			value = fields[1]
		}
		if len(fields) > 2 || value != "on" && value != "off" {
			setNotice(&attached.feedback, presentationTime(clock), "Use /live, /live on, or /live off", true)
			return true, nil
		}
		recordDraft(attached, (*editor), clock, session.ownedImages, &session.questions, &session.approvals, side)
		(*editor).Text, (*editor).CursorBack, (*editor).Images = "", 0, nil
		(*editor).Changed()
		setLivePane(attached, clock, value)
		return true, nil
	}
	target, selected := selectedSettings(attached, session.Client.Requests, session.settings)
	if command == "/permissions" && (target != nil || attached.presentation.DisplayingSide()) {
		setNotice(&attached.feedback, presentationTime(clock), "Return to Main to change permissions", false)
		return true, nil
	}
	if command == "/permissions" && session.Client.Settings.Pending() {
		setNotice(&attached.feedback, presentationTime(clock), "Settings update pending", false)
		return true, nil
	}
	choices, current := settingsChoices(command, session.models, selected, session.serviceTiers, session.Client.Thread)
	if len(fields) == 1 {
		session.deleteDraftRange(attached, picker, editor, side, clock, overlay, 0, len((*editor).Text))
		picker.showSettingsPicker(session.modelsLoading, command, choices, current)
		picker.settingThread = attached.presentation.View().Input.ID
		if command == "/permissions" {
			picker.permissionSelection = session.Client.Settings.BeginPermissionSelection()
			picker.Loading, picker.Problem = true, ""
			if err := picker.requestPermissions(attached, &session.Client, ""); err != nil {
				picker.Loading, picker.Problem = false, "Permission choices unavailable: "+err.Error()
			}
		}
		return true, nil
	}
	if command != "/model" && command != "/permissions" && len(fields) != 2 {
		setNotice(&attached.feedback, presentationTime(clock), "Use "+command+" or "+command+" VALUE", true)
		return true, nil
	}
	if target == nil && session.Client.Settings.Pending() || target != nil && target.Settings.Pending() {
		setNotice(&attached.feedback, presentationTime(clock), "Settings update pending", false)
		return true, nil
	}
	change := make(map[string]any)
	switch command {
	case "/model":
		var err error
		change, err = sessioncontrol.ParseModelCommand(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), command)))
		if err != nil {
			setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
			return true, nil
		}
		if _, speed := change["serviceTier"]; speed && target != nil {
			setNotice(&attached.feedback, presentationTime(clock), "Return to Main to change service tier", false)
			return true, nil
		}
	case "/permissions":
		var err error
		change, err = sessioncontrol.ParsePermissionCommand(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), command)))
		if err != nil {
			setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
			return true, nil
		}
	case "/reasoning", "/effort":
		change["effort"] = fields[1]
	case "/tier":
		change["serviceTier"] = fields[1]
		if fields[1] == "default" {
			change["serviceTier"] = nil
		}
	}
	// The host validates explicit values, including custom models and tiers.
	accepted, err := updateSettings(attached, &session.Client, selected, &session.serviceTiers, session.panes, session.restoring != nil, clock, change)
	if accepted {
		notice, alert := attached.feedback.Text, attached.feedback.Alert
		recordDraft(attached, (*editor), clock, session.ownedImages, &session.questions, &session.approvals, side)
		(*editor).Text, (*editor).CursorBack, (*editor).Images = "", 0, nil
		(*editor).Changed()
		setNotice(&attached.feedback, presentationTime(clock), notice, alert)
	}
	return true, err
}

func (session *nativeSession) settingsMessage(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, side *sessioncontrol.Side, clock func() time.Time, overlay bool, method string, m appserver.Message) (bool, error) {
	if method == "model/list" {
		if m.Error != nil {
			session.modelsLoading = false
			problem := "Model choices unavailable · Esc to close; use an explicit VALUE"
			refreshSettingsPicker(picker, attached.presentation.View().Input.ID, nil, preferences.Applied{}, session.serviceTiers, problem)

			attached.hooks.modelsReady(nil, preferences.Applied{}, session.serviceTiers, problem)

			if pending := attached.reasoning; pending != nil && pending.attached.executionPresentation == attached.executionPresentation {
				setNotice(&pending.attached.feedback, presentationTime(clock), problem, true)
			}
			attached.reasoning = nil
			setNotice(&attached.feedback, presentationTime(clock), "Model choices unavailable: "+m.Error.Message, true)
			return true, nil
		}
		var result struct {
			Data       []appServerModel `json:"data"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			return true, err
		}
		session.models = append(session.models, result.Data...)
		if result.NextCursor != "" {
			return true, session.Client.Request("model/list", map[string]any{"cursor": result.NextCursor, "includeHidden": true})
		}
		session.modelsLoading = false
		applied := session.settings
		_, selected := selectedSettings(attached, session.Client.Requests, applied)
		refreshSettingsPicker(picker, attached.presentation.View().Input.ID, session.models, selected, session.serviceTiers, "")

		attached.hooks.modelsReady(session.models, applied, session.serviceTiers, "")

		if pending := attached.reasoning; pending != nil {
			attached.reasoning = nil
			if pending.attached.executionPresentation != attached.executionPresentation || pending.attached.presentation == nil {
				return true, nil
			}
			if pending.thread != pending.attached.presentation.View().Input.ID {
				setNotice(&pending.attached.feedback, presentationTime(clock), "Settings target changed · retry the reasoning control", false)
				return true, nil
			}
			return true, stepReasoningForTurn(pending.attached, &session.Client, applied, &session.serviceTiers, session.panes, session.restoring != nil, clock, session.models, pending.up, &pending.turn)
		}
		return true, nil
	}
	if method == "thread/settings/update" || method == "turn/settings/update" {
		ack, err := session.Client.Settings.Acknowledge(method, m.Result, m.Error != nil)
		if err != nil {
			return true, err
		}
		failure := ""
		if m.Error != nil {
			failure = m.Error.Message
		}
		applyInputEffects(attached, &session.questions, &session.approvals, picker, session.Client.Turn, clock, attached.presentation.SettingsAcknowledged(attached.conversation, method, failure, ack))
		return true, nil
	}
	if m.Method != "thread/settings/updated" {
		return false, nil
	}
	var event struct {
		ThreadID string `json:"threadId"`
		Settings struct {
			sessioncontrol.PermissionState
			Provider string         `json:"modelProvider"`
			Approval jsontext.Value `json:"approvalPolicy"`
			Sandbox  *struct {
				Type string `json:"type"`
			} `json:"sandboxPolicy"`
			Cwd         string `json:"cwd"`
			Model       string `json:"model"`
			Effort      string `json:"effort"`
			ServiceTier string `json:"serviceTier"`
		} `json:"threadSettings"`
	}
	if err := json.Unmarshal(m.Params, &event); err != nil {
		return true, fmt.Errorf("thread settings: %w", err)
	}
	if event.ThreadID != session.Client.Thread {
		if attached.session.KnownCaller(session.Client.Thread) != "" && event.ThreadID != "" {
			agent := attached.session.Agent(attached.session.Caller(event.ThreadID))
			agent.Settings = preferences.Evidence{Model: event.Settings.Model, Effort: event.Settings.Effort, Tier: event.Settings.ServiceTier, TierKnown: true}
			agent.SettingsLive = true
		}
		return true, nil
	}
	if event.Settings.Provider != "" {
		session.statusConfig.Provider = event.Settings.Provider
	}
	if len(event.Settings.Approval) > 0 {
		session.statusConfig.Approval = event.Settings.Approval
		if err := json.Unmarshal(event.Settings.Approval, &event.Settings.PermissionState.Approval); err != nil {
			return true, err
		}
	}
	if event.Settings.Sandbox != nil {
		session.statusConfig.Sandbox.Type = event.Settings.Sandbox.Type
	}
	if event.Settings.Cwd != "" {
		attached.cwd = event.Settings.Cwd
	}
	session.settings.Model, session.settings.Effort, session.settings.Tier = event.Settings.Model, event.Settings.Effort, event.Settings.ServiceTier
	session.Client.Settings.Permissions = event.Settings.PermissionState
	confirmation := session.Client.Settings.Confirmed(session.Client.Turn, session.settings.Effort)
	if confirmation.ChooseTier {
		session.serviceTiers = chooseServiceTier(session.serviceTiers, session.settings.Model, session.Client.Thread, session.settings.Tier)
	}
	paneError(attached.source, session.panes.retainAppliedSettings(preferences.Scope{Workspace: attached.cwd, Thread: session.Client.Thread}, session.Client.Settings.RestoreEffort, preferences.Applied{Model: session.settings.Model, Effort: session.settings.Effort, Tier: session.settings.Tier, Observed: presentationTime(clock)}))
	if !confirmation.Requested {
		return true, nil
	}
	decision := confirmation.Update
	if decision.Action == sessioncontrol.SettingsTurn {
		if err := session.Client.Requests.ApplySettings(&session.Client.Live, decision); err != nil {
			return true, err
		}
		setNotice(&attached.feedback, presentationTime(clock), "Settings saved · updating active turn…", false)
	} else {
		setNotice(&attached.feedback, presentationTime(clock), "Settings saved for next turn", false)
		if session.Client.Settings.RestoreEffort {
			setNotice(&attached.feedback, presentationTime(clock), "Default reasoning still needs restoration · choose /effort VALUE or restart resume", true)
		}
	}
	return true, nil
}

// Settings share the composer's bounded viewport, never the transcript.
func (p *composerPicker) showSettingsPicker(loading bool, command string, choices []string, current string) {
	p.cancelScan()
	p.openMenu("settings")
	p.Target, p.Problem, p.Loading = completion.Target{}, "", loading
	p.Choices = nil
	p.Setting = command
	if command == "/tier" {
		if current == "" {
			current = "default"
		}
		if !slices.Contains(choices, current) {
			choices = append(choices, current)
		}
	}
	if p.Loading {
		return
	}
	for _, value := range choices {
		choice := completion.Choice{Name: value}
		if value == current {
			choice.Description = "Current"
			p.Preferred = choice.Identity()
		}
		p.Choices = append(p.Choices, choice)
	}
	if len(p.Choices) == 0 {
		p.Problem = "No advertised choices · Esc to close; use an explicit VALUE"
	}
}

func selectedServiceTier(tiers *inferencegateway.Tiers, model, thread string) string {
	tier, _ := tiers.Choice(model, thread)
	return tier
}

func chooseServiceTier(tiers *inferencegateway.Tiers, model, thread, tier string) *inferencegateway.Tiers {
	if tiers == nil {
		tiers = inferencegateway.NewTiers(nil)
	}
	tiers.Confirm(model, thread, tier)
	return tiers
}

func chooseSetting(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], applied preferences.Applied, tiers **inferencegateway.Tiers, panes *nativePreferences, restoring bool, clock func() time.Time, picker *composerPicker, choice completion.Choice) {
	p := picker
	if p.Loading {
		return
	}
	if picker.settingThread != "" && picker.settingThread != attached.presentation.View().Input.ID {
		p.Problem = "Settings target changed · reopen the picker"
		return
	}
	if picker.Setting == "/permissions" {
		choosePermission(attached, control, applied, tiers, panes, restoring, clock, picker, choice)
		return
	}
	field := "model"
	switch picker.Setting {
	case "/effort", "/reasoning":
		field = "effort"
	case "/tier":
		field = "serviceTier"
	}
	var value any = choice.Name
	if field == "serviceTier" && value == "default" {
		value = nil
	}
	accepted, err := updateSettings(attached, control, applied, tiers, panes, restoring, clock, map[string]any{field: value})
	if err != nil {
		setNotice(&attached.feedback, presentationTime(clock), "Settings update failed: "+err.Error(), true)
	}
	if accepted {
		p.Kind, p.Open, p.Choices = "", false, nil
		picker.Setting = ""
	} else {
		p.Problem = attached.feedback.Text
	}
}

// setLivePane changes presentation without stopping preview or capture updates.
func setLivePane(attached *attachedPresentation, clock func() time.Time, value string) {
	attached.liveHidden = value == "off"

	setNotice(&attached.feedback, presentationTime(clock), "Live pane "+value+" for this session", false)
}
