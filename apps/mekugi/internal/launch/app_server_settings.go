package launch

import (
	"encoding/json/jsontext"
	"time"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/composer"

	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

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

// Codex owns configuration_update history items. These RPCs submit settings
// intent, never synthetic Responses items or writes to the user's config file.
// Source: codex-rs/app-server-protocol/src/protocol/v2/{thread,turn}.rs
func updateSettings(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], applied preferences.Applied, tiers **inferencegateway.Tiers, panes *nativePreferences, restoring bool, clock func() time.Time, change map[string]any) (bool, error) {
	decision := control.PrepareSettings(applied, change, restoring)
	switch decision.Action {
	case sessioncontrol.SettingsUnavailable:
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the thread or turn to finish starting", true)
		return false, nil
	case sessioncontrol.SettingsAlreadyPending:
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Settings update pending", false)
		return false, nil
	}
	if err := control.Requests.ApplySettings(&control.Live, decision); err != nil {
		return false, err
	}

	if decision.RestoreDefault {
		paneError(attached.source, panes.retainAppliedSettings(terminal, preferences.Scope{Workspace: attached.cwd, Thread: control.Thread}, control.Settings.RestoreEffort, preferences.Applied{Model: applied.Model, Effort: applied.Effort, Tier: applied.Tier, Observed: presentationTime(clock)}))
	}
	if decision.ChooseTier {
		(*tiers) = chooseServiceTier((*tiers), applied.Model, control.Thread, applied.Tier)
	}
	switch decision.Action {
	case sessioncontrol.SettingsUnchanged:
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Settings unchanged", false)
		if decision.ChooseTier {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Service tier updated", false)
		}
	case sessioncontrol.SettingsTurn:
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Updating active turn…", false)
	case sessioncontrol.SettingsThread:
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Updating settings…", false)
	}
	return true, nil
}

func stepReasoning(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], applied preferences.Applied, tiers **inferencegateway.Tiers, panes *nativePreferences, restoring bool, clock func() time.Time, models []appServerModel, loading bool, reasoningKey **bool, up bool) error {
	if loading {
		(*reasoningKey) = new(up)
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Loading model choices…", false)
		return nil
	}
	choices := effortChoices(models, applied.Model)
	if len(choices) == 0 {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Reasoning choices unavailable · use /reasoning VALUE", true)
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
	_, err := updateSettings(attached, terminal, control, applied, tiers, panes, restoring, clock, map[string]any{"effort": choices[next]})
	return err
}

// Slash controls work while a turn runs and do not submit conversation input.
func settingsCommand(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], applied preferences.Applied, tiers **inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, owned map[string]bool, panes *nativePreferences, models []appServerModel, loading bool, text string) (bool, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false, nil
	}
	command := fields[0]
	if command != "/model" && command != "/reasoning" && command != "/effort" && command != "/tier" && command != "/live" {
		return false, nil
	}
	if control.Replacement.Pending() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the new session · draft kept", false)
		return true, nil
	}
	if command == "/live" {
		value := "off"
		if terminal.LiveHidden {
			value = "on"
		}
		if len(fields) == 2 {
			value = fields[1]
		}
		if len(fields) > 2 || value != "on" && value != "off" {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Use /live, /live on, or /live off", true)
			return true, nil
		}
		recordDraft(attached, (*editor), terminal, clock, owned, questions, approvals, side)
		(*editor).Text, (*editor).CursorBack, (*editor).Images = "", 0, nil
		(*editor).Changed()
		setLivePane(&attached.feedback, terminal, clock, value)
		return true, nil
	}
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
		current = applied.Effort
		choices = effortChoices(models, applied.Model)
	case "/tier":
		current = (*tiers).Effective(applied.Model, applied.Tier, control.Thread)
		choices = []string{"default"}
		if model := currentModel(models, applied.Model); model != nil {
			for _, tier := range model.Tiers {
				choices = append(choices, tier.ID)
			}
		}
	}
	if len(fields) == 1 {
		deleteDraftRange(attached, terminal, control, picker, editor, childKey, applied, (*tiers), title, backend, restoring, side, clock, questions, approvals, overlay, owned, 0, len((*editor).Text))
		picker.showSettingsPicker(loading, command, choices, current)
		return true, nil
	}
	if len(fields) != 2 {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Use "+command+" or "+command+" VALUE", true)
		return true, nil
	}
	if control.Settings.Pending() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Settings update pending", false)
		return true, nil
	}
	change := make(map[string]any)
	switch command {
	case "/model":
		change["model"] = fields[1]
	case "/reasoning", "/effort":
		change["effort"] = fields[1]
	case "/tier":
		change["serviceTier"] = fields[1]
		if fields[1] == "default" {
			change["serviceTier"] = nil
		}
	}
	// The host validates explicit values, including custom models and tiers.
	accepted, err := updateSettings(attached, terminal, control, applied, tiers, panes, restoring, clock, change)
	if accepted {
		notice, alert := attached.feedback.Text, attached.feedback.Alert
		recordDraft(attached, (*editor), terminal, clock, owned, questions, approvals, side)
		(*editor).Text, (*editor).CursorBack, (*editor).Images = "", 0, nil
		(*editor).Changed()
		setNotice(&attached.feedback, terminal, presentationTime(clock), notice, alert)
	}
	return true, err
}

func settingsMessage(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, childKey *scope.Key[*appServerChildHistory], tiers **inferencegateway.Tiers, title string, backend string, restoring bool, side *sessioncontrol.Side, clock func() time.Time, questions *nativeQuestionDock, approvals *nativeApprovalDock, overlay bool, owned map[string]bool, panes *nativePreferences, model *string, effort *string, tier *string, models *[]appServerModel, loading *bool, reasoningKey **bool, status *appServerStatusConfig, method string, m appserver.Message) (bool, error) {
	if method == "model/list" {
		if m.Error != nil {
			(*loading), (*reasoningKey) = false, nil
			if picker.Kind == "settings" {
				picker.Loading = false
				picker.Problem = "Model choices unavailable · Esc to close; use an explicit VALUE"
			}
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Model choices unavailable: "+m.Error.Message, true)
			return true, nil
		}
		var result struct {
			Data       []appServerModel `json:"data"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			return true, err
		}
		(*models) = append((*models), result.Data...)
		if result.NextCursor != "" {
			return true, control.Request("model/list", map[string]any{"cursor": result.NextCursor, "includeHidden": true})
		}
		(*loading) = false
		if picker.Setting != "" {
			command := picker.Setting
			if picker.Kind == "settings" {
				_, _ = settingsCommand(attached, terminal, control, picker, editor, childKey, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, title, backend, restoring, side, clock, questions, approvals, overlay, owned, panes, (*models), (*loading), command)
			}
		}
		if (*reasoningKey) != nil {
			up := *(*reasoningKey)
			(*reasoningKey) = nil
			return true, stepReasoning(attached, terminal, control, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier)}, tiers, panes, restoring, clock, (*models), (*loading), reasoningKey, up)
		}
		return true, nil
	}
	if method == "thread/settings/update" || method == "turn/settings/update" {
		ack, err := control.Settings.Acknowledge(method, m.Result, m.Error != nil)
		if err != nil {
			return true, err
		}
		failure := ""
		if m.Error != nil {
			failure = m.Error.Message
		}
		applyInputEffects(attached, terminal, questions, approvals, picker, control.Turn, clock, attached.presentation.SettingsAcknowledged(attached.conversation, method, failure, ack))
		return true, nil
	}
	if m.Method != "thread/settings/updated" {
		return false, nil
	}
	var event struct {
		ThreadID string `json:"threadId"`
		Settings struct {
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
	if event.ThreadID != control.Thread {
		if attached.session.KnownCaller(control.Thread) != "" && event.ThreadID != "" {
			agent := attached.session.Agent(attached.session.Caller(event.ThreadID))
			agent.Settings = preferences.Evidence{Model: event.Settings.Model, Effort: event.Settings.Effort, Tier: event.Settings.ServiceTier, TierKnown: true}
			agent.SettingsLive = true
		}
		return true, nil
	}
	if event.Settings.Provider != "" {
		status.Provider = event.Settings.Provider
	}
	if len(event.Settings.Approval) > 0 {
		status.Approval = event.Settings.Approval
	}
	if event.Settings.Sandbox != nil {
		status.Sandbox.Type = event.Settings.Sandbox.Type
	}
	if event.Settings.Cwd != "" {
		attached.cwd = event.Settings.Cwd
	}
	(*model), (*effort), (*tier) = event.Settings.Model, event.Settings.Effort, event.Settings.ServiceTier
	confirmation := control.Settings.Confirmed(control.Turn, (*effort))
	if confirmation.ChooseTier {
		(*tiers) = chooseServiceTier((*tiers), (*model), control.Thread, (*tier))
	}
	paneError(attached.source, panes.retainAppliedSettings(terminal, preferences.Scope{Workspace: attached.cwd, Thread: control.Thread}, control.Settings.RestoreEffort, preferences.Applied{Model: (*model), Effort: (*effort), Tier: (*tier), Observed: presentationTime(clock)}))
	if !confirmation.Requested {
		return true, nil
	}
	decision := confirmation.Update
	if decision.Action == sessioncontrol.SettingsTurn {
		if err := control.Requests.ApplySettings(&control.Live, decision); err != nil {
			return true, err
		}
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Settings saved · updating active turn…", false)
	} else {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Settings saved for next turn", false)
		if control.Settings.RestoreEffort {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Default reasoning still needs restoration · choose /effort VALUE or restart resume", true)
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

func chooseSetting(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], applied preferences.Applied, tiers **inferencegateway.Tiers, panes *nativePreferences, restoring bool, clock func() time.Time, picker *composerPicker, choice completion.Choice) {
	p := picker
	if p.Loading {
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
	accepted, err := updateSettings(attached, terminal, control, applied, tiers, panes, restoring, clock, map[string]any{field: value})
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Settings update failed: "+err.Error(), true)
	}
	if accepted {
		p.Kind, p.Open, p.Choices = "", false, nil
		picker.Setting = ""
	} else {
		p.Problem = attached.feedback.Text
	}
}

// setLivePane changes presentation without stopping preview or capture updates.
func setLivePane(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, value string) {
	terminal.LiveHidden = value == "off"

	setNotice(feedback, terminal, presentationTime(clock), "Live pane "+value+" for this session", false)
}
