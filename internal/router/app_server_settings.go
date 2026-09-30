package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/appserver"
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
	if u.thread == "" || u.restoring != nil || u.starting || u.clearing {
		u.setNotice("Wait for the thread or turn to finish starting", true)
		return false, nil
	}
	if u.settingsPending {
		u.setNotice("Settings update pending", false)
		return false, nil
	}
	unchanged := true
	for field, value := range change {
		switch field {
		case "collaborationMode":
			// Resume passes the host-returned snapshot with only its effort
			// cleared. A changed complete mode requires host confirmation.
			unchanged = false
		case "model":
			unchanged = unchanged && value == u.model
		case "effort":
			unchanged = unchanged && (value == u.reasoningEffort || value == nil && u.reasoningEffort == "")
		case "serviceTier":
			// Codex normalizes the legacy fast spelling to priority.
			// Source: codex-rs/core/src/session/step_settings.rs:274:283@86be5320
			unchanged = unchanged && (value == u.serviceTier || value == "fast" && u.serviceTier == "priority" || value == nil && (u.serviceTier == "" || u.serviceTier == "default"))
		}
	}
	params := map[string]any{"threadId": u.thread}
	maps.Copy(params, change)
	// Codex suppresses unchanged thread/settings/updated notifications. A no-op
	// needs no thread update, but can still repair a previously rejected live one.
	if unchanged {
		if u.turn == "" {
			if _, explicit := change["effort"]; explicit && u.resumeClearEffort {
				u.resumeClearEffort = false
				u.retainAppliedSettings()
			}
			u.setNotice("Settings unchanged", false)
			return true, nil
		}
		params["turnId"] = u.turn
		if err := u.request("turn/settings/update", params); err != nil {
			return false, err
		}
		u.settingsPending = true
		u.setNotice("Updating active turn…", false)
		return true, nil
	}
	if err := u.request("thread/settings/update", params); err != nil {
		return false, err
	}
	u.settingsPending, u.settingsChange, u.settingsTurn = true, change, u.turn
	u.setNotice("Updating settings…", false)
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
	if u.clearing {
		u.setNotice("Wait for the new session · draft kept", false)
		return true, nil
	}
	if command == "/live" {
		u.ensureShell()
		value := "off"
		if u.shell.liveHidden {
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
		u.draft, u.cursorBack, u.images = "", 0, nil
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
		u.deleteDraftRange(0, len(u.draft))
		u.showSettingsPicker(command, choices)
		return true, nil
	}
	if len(fields) != 2 {
		u.setNotice("Use "+command+" or "+command+" VALUE", true)
		return true, nil
	}
	if u.settingsPending {
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
		notice, alert := u.notice, u.noticeAlert
		u.recordDraft()
		u.draft, u.cursorBack, u.images = "", 0, nil
		u.setNotice(notice, alert)
	}
	return true, err
}

func (u *appServerUI) settingsMessage(method string, m appserver.Message) (bool, error) {
	if method == "model/list" {
		if m.Error != nil {
			u.modelsLoading, u.reasoningKey = false, nil
			if u.picker.modal == "settings" {
				u.picker.loading = false
				u.picker.problem = "Model choices unavailable · Esc to close; use an explicit VALUE"
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
			return true, u.request("model/list", map[string]any{"cursor": result.NextCursor, "includeHidden": true})
		}
		u.modelsLoading = false
		if u.settingsChoices != "" {
			command := u.settingsChoices
			if u.picker.modal == "settings" {
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
		if m.Error != nil {
			u.settingsPending, u.settingsChange = false, nil
			if u.resumeClearEffort {
				u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
				u.unsent, u.queued = nil, nil
				u.setNotice("Could not restore default reasoning: "+m.Error.Message+" · choose /effort VALUE or restart resume", true)
			} else {
				u.setNotice(method+": "+m.Error.Message, true)
			}
			return true, nil
		}
		// Thread RPC acknowledges enqueueing only; the notification is authoritative.
		if method == "turn/settings/update" {
			var result struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(m.Result, &result); err != nil {
				return true, err
			}
			u.settingsPending = false
			switch result.Status {
			case "applied":
				u.setNotice("Settings saved · live update published for subsequent steps", false)
			case "targetUnavailable":
				u.setNotice("Settings saved for next turn · live target finished", false)
			default:
				u.setNotice("Settings saved · unknown live update status: "+result.Status, true)
			}
		}
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
	if event.ThreadID != u.thread {
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
		u.session.cwd = event.Settings.Cwd
	}
	u.model, u.reasoningEffort, u.serviceTier = event.Settings.Model, event.Settings.Effort, event.Settings.ServiceTier
	if _, explicit := u.settingsChange["effort"]; explicit || u.reasoningEffort == "" {
		u.resumeClearEffort = false
	}
	u.retainAppliedSettings()
	if !u.settingsPending || u.settingsChange == nil {
		return true, nil
	}
	change := u.settingsChange
	u.settingsChange = nil
	if u.settingsTurn != "" && u.settingsTurn == u.turn {
		change["threadId"], change["turnId"] = u.thread, u.settingsTurn
		if err := u.request("turn/settings/update", change); err != nil {
			return true, err
		}
		u.setNotice("Settings saved · updating active turn…", false)
	} else {
		u.settingsPending = false
		u.setNotice("Settings saved for next turn", false)
		if u.resumeClearEffort {
			u.setNotice("Default reasoning still needs restoration · choose /effort VALUE or restart resume", true)
		}
	}
	return true, nil
}

// Settings share the composer's bounded viewport, never the transcript.
func (u *appServerUI) showSettingsPicker(command string, choices []string) {
	u.cancelPickerScan()
	p := &u.picker
	p.modal, p.open, p.selected, p.top = "settings", true, 0, 0
	p.target, p.problem, p.loading = composerTarget{}, "", u.modelsLoading
	p.choices = nil
	u.settingsChoices = command
	current := u.model
	switch command {
	case "/reasoning", "/effort":
		current = u.reasoningEffort
	case "/tier":
		current = u.serviceTier
		if current == "" {
			current = "default"
		}
	}
	if p.loading {
		return
	}
	for _, value := range choices {
		choice := composerChoice{name: value}
		if value == current {
			choice.description = "Current"
			p.selected = len(p.choices)
		}
		p.choices = append(p.choices, choice)
	}
	if len(p.choices) == 0 {
		p.problem = "No advertised choices · Esc to close; use an explicit VALUE"
	}
}

func (u *appServerUI) settingsPickerKey(key string) bool {
	p := &u.picker
	switch key {
	case "\x1b[200~", "\x1b[201~":
		return false
	case "\x1b", "\x03":
		p.modal, p.open, p.choices = "", false, nil
		u.settingsChoices = ""
	case "\x1b[A", "\x1bOA", "\x10", "\x1b[B", "\x1bOB", "\x0e", "\t":
		if n := len(p.choices); n > 0 {
			step := 1
			if key == "\x1b[A" || key == "\x1bOA" || key == "\x10" {
				step = -1
			}
			p.selected = (p.selected + step + n) % n
		}
	case "\x1b[5~":
		p.selected = max(0, p.selected-8)
	case "\x1b[6~":
		p.selected = max(0, min(len(p.choices)-1, p.selected+8))
	case "\x1b[H", "\x1bOH":
		p.selected = 0
	case "\x1b[F", "\x1bOF":
		p.selected = max(0, len(p.choices)-1)
	case "\r":
		if p.loading || len(p.choices) == 0 {
			return true
		}
		field := "model"
		switch u.settingsChoices {
		case "/effort", "/reasoning":
			field = "effort"
		case "/tier":
			field = "serviceTier"
		}
		var value any = p.choices[p.selected].name
		if field == "serviceTier" && value == "default" {
			value = nil
		}
		accepted, err := u.updateSettings(map[string]any{field: value})
		if err != nil {
			u.setNotice("Settings update failed: "+err.Error(), true)
		}
		if accepted {
			p.modal, p.open, p.choices = "", false, nil
			u.settingsChoices = ""
		} else {
			p.problem = u.notice
		}
	}
	return true
}

// setLivePane changes presentation without stopping preview or capture updates.
func (u *appServerUI) setLivePane(value string) {
	u.ensureShell()
	u.shell.liveHidden = value == "off"
	u.dirty = true
	u.setNotice("Live pane "+value+" for this session", false)
}
