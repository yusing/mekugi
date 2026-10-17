package router

import (
	"encoding/json/jsontext"

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

func (u *appServerUI) currentModel() *appServerModel {
	for i := range u.models {
		if u.models[i].Model == u.model {
			return &u.models[i]
		}
	}
	return nil
}

func (u *appServerUI) effortChoices() []string {
	var choices []string
	if model := u.currentModel(); model != nil {
		for _, effort := range model.Efforts {
			choices = append(choices, effort.Effort)
		}
	}
	return choices
}

// Codex owns configuration_update history items. These RPCs submit settings
// intent, never synthetic Responses items or writes to the user's config file.
// Source: codex-rs/app-server-protocol/src/protocol/v2/{thread,turn}.rs
func (u *appServerUI) updateSettings(change map[string]any) (bool, error) {
	decision := u.PrepareSettings(preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, change, u.restoring != nil)
	switch decision.Action {
	case sessioncontrol.SettingsUnavailable:
		u.setNotice("Wait for the thread or turn to finish starting", true)
		return false, nil
	case sessioncontrol.SettingsAlreadyPending:
		u.setNotice("Settings update pending", false)
		return false, nil
	}
	if err := u.Requests.ApplySettings(&u.Live, decision); err != nil {
		return false, err
	}

	if decision.RestoreDefault {
		u.retainAppliedSettings()
	}
	if decision.ChooseTier {
		u.chooseServiceTier()
	}
	switch decision.Action {
	case sessioncontrol.SettingsUnchanged:
		u.setNotice("Settings unchanged", false)
		if decision.ChooseTier {
			u.setNotice("Service tier updated", false)
		}
	case sessioncontrol.SettingsTurn:
		u.setNotice("Updating active turn…", false)
	case sessioncontrol.SettingsThread:
		u.setNotice("Updating settings…", false)
	}
	return true, nil
}

func (u *appServerUI) stepReasoning(up bool) error {
	if u.modelsLoading {
		u.reasoningKey = new(up)
		u.setNotice("Loading model choices…", false)
		return nil
	}
	choices := u.effortChoices()
	if len(choices) == 0 {
		u.setNotice("Reasoning choices unavailable · use /reasoning VALUE", true)
		return nil
	}
	index := slices.Index(choices, u.reasoningEffort)
	if index < 0 {
		index = slices.Index(choices, u.currentModel().DefaultEffort)
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
	_, err := u.updateSettings(map[string]any{"effort": choices[next]})
	return err
}

// Slash controls work while a turn runs and do not submit conversation input.
func (u *appServerUI) settingsCommand(text string) (bool, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false, nil
	}
	command := fields[0]
	if command != "/model" && command != "/reasoning" && command != "/effort" && command != "/tier" && command != "/live" {
		return false, nil
	}
	if u.Replacement.Pending() {
		u.setNotice("Wait for the new session · draft kept", false)
		return true, nil
	}
	if command == "/live" {
		value := "off"
		if u.shell.LiveHidden {
			value = "on"
		}
		if len(fields) == 2 {
			value = fields[1]
		}
		if len(fields) > 2 || value != "on" && value != "off" {
			u.setNotice("Use /live, /live on, or /live off", true)
			return true, nil
		}
		u.recordDraft()
		u.Text, u.CursorBack, u.Images = "", 0, nil
		u.Editor.Changed()
		u.setLivePane(value)
		return true, nil
	}
	var choices []string
	switch command {
	case "/model":
		for _, model := range u.models {
			if !model.Hidden {
				choices = append(choices, model.Model)
			}
		}
	case "/reasoning", "/effort":
		choices = u.effortChoices()
	case "/tier":
		choices = []string{"default"}
		if model := u.currentModel(); model != nil {
			for _, tier := range model.Tiers {
				choices = append(choices, tier.ID)
			}
		}
	}
	if len(fields) == 1 {
		u.deleteDraftRange(0, len(u.Text))
		u.showSettingsPicker(command, choices)
		return true, nil
	}
	if len(fields) != 2 {
		u.setNotice("Use "+command+" or "+command+" VALUE", true)
		return true, nil
	}
	if u.Settings.Pending() {
		u.setNotice("Settings update pending", false)
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
	accepted, err := u.updateSettings(change)
	if accepted {
		notice, alert := u.attached.feedback.Text, u.attached.feedback.Alert
		u.recordDraft()
		u.Text, u.CursorBack, u.Images = "", 0, nil
		u.Editor.Changed()
		u.setNotice(notice, alert)
	}
	return true, err
}

func (u *appServerUI) settingsMessage(method string, m appserver.Message) (bool, error) {
	if method == "model/list" {
		if m.Error != nil {
			u.modelsLoading, u.reasoningKey = false, nil
			if u.picker.Kind == "settings" {
				u.picker.Loading = false
				u.picker.Problem = "Model choices unavailable · Esc to close; use an explicit VALUE"
			}
			u.setNotice("Model choices unavailable: "+m.Error.Message, true)
			return true, nil
		}
		var result struct {
			Data       []appServerModel `json:"data"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			return true, err
		}
		u.models = append(u.models, result.Data...)
		if result.NextCursor != "" {
			return true, u.Request("model/list", map[string]any{"cursor": result.NextCursor, "includeHidden": true})
		}
		u.modelsLoading = false
		if u.picker.Setting != "" {
			command := u.picker.Setting
			if u.picker.Kind == "settings" {
				_, _ = u.settingsCommand(command)
			}
		}
		if u.reasoningKey != nil {
			up := *u.reasoningKey
			u.reasoningKey = nil
			return true, u.stepReasoning(up)
		}
		return true, nil
	}
	if method == "thread/settings/update" || method == "turn/settings/update" {
		ack, err := u.Settings.Acknowledge(method, m.Result, m.Error != nil)
		if err != nil {
			return true, err
		}
		failure := ""
		if m.Error != nil {
			failure = m.Error.Message
		}
		u.applyInputEffects(u.attached.presentation.SettingsAcknowledged(u.attached.conversation, method, failure, ack))
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
	if event.ThreadID != u.Thread {
		if u.attached.session.KnownCaller(u.Thread) != "" && event.ThreadID != "" {
			agent := u.attached.session.Agent(u.attached.session.Caller(event.ThreadID))
			agent.Settings = preferences.Evidence{Model: event.Settings.Model, Effort: event.Settings.Effort, Tier: event.Settings.ServiceTier, TierKnown: true}
			agent.SettingsLive = true
		}
		return true, nil
	}
	if event.Settings.Provider != "" {
		u.statusConfig.Provider = event.Settings.Provider
	}
	if len(event.Settings.Approval) > 0 {
		u.statusConfig.Approval = event.Settings.Approval
	}
	if event.Settings.Sandbox != nil {
		u.statusConfig.Sandbox.Type = event.Settings.Sandbox.Type
	}
	if event.Settings.Cwd != "" {
		u.attached.cwd = event.Settings.Cwd
	}
	u.model, u.reasoningEffort, u.serviceTier = event.Settings.Model, event.Settings.Effort, event.Settings.ServiceTier
	confirmation := u.Settings.Confirmed(u.Turn, u.reasoningEffort)
	if confirmation.ChooseTier {
		u.chooseServiceTier()
	}
	u.retainAppliedSettings()
	if !confirmation.Requested {
		return true, nil
	}
	decision := confirmation.Update
	if decision.Action == sessioncontrol.SettingsTurn {
		if err := u.Requests.ApplySettings(&u.Live, decision); err != nil {
			return true, err
		}
		u.setNotice("Settings saved · updating active turn…", false)
	} else {
		u.setNotice("Settings saved for next turn", false)
		if u.Settings.RestoreEffort {
			u.setNotice("Default reasoning still needs restoration · choose /effort VALUE or restart resume", true)
		}
	}
	return true, nil
}

// Settings share the composer's bounded viewport, never the transcript.
func (u *appServerUI) showSettingsPicker(command string, choices []string) {
	u.picker.cancelScan()
	p := &u.picker
	u.openMenu("settings")
	p.Target, p.Problem, p.Loading = completion.Target{}, "", u.modelsLoading
	p.Choices = nil
	u.picker.Setting = command
	current := u.model
	switch command {
	case "/reasoning", "/effort":
		current = u.reasoningEffort
	case "/tier":
		current = u.displayServiceTier()
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

func (u *appServerUI) selectedServiceTier() string {
	tier, _ := u.serviceTiers.Choice(u.model, u.Thread)
	return tier
}

func (u *appServerUI) chooseServiceTier() {
	if u.serviceTiers == nil {
		u.serviceTiers = inferencegateway.NewTiers(nil)
	}
	u.serviceTiers.Confirm(u.model, u.Thread, u.serviceTier)
}

func (u *appServerUI) chooseSetting(choice completion.Choice) {
	p := &u.picker
	if p.Loading {
		return
	}
	field := "model"
	switch u.picker.Setting {
	case "/effort", "/reasoning":
		field = "effort"
	case "/tier":
		field = "serviceTier"
	}
	var value any = choice.Name
	if field == "serviceTier" && value == "default" {
		value = nil
	}
	accepted, err := u.updateSettings(map[string]any{field: value})
	if err != nil {
		u.setNotice("Settings update failed: "+err.Error(), true)
	}
	if accepted {
		p.Kind, p.Open, p.Choices = "", false, nil
		u.picker.Setting = ""
	} else {
		p.Problem = u.attached.feedback.Text
	}
}

// setLivePane changes presentation without stopping preview or capture updates.
func (u *appServerUI) setLivePane(value string) {
	u.shell.LiveHidden = value == "off"
	u.dirty = true
	u.setNotice("Live pane "+value+" for this session", false)
}
