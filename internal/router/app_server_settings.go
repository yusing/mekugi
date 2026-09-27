package router

import (
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
	if u.thread == "" || u.restoring != nil || u.starting {
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
		case "model":
			unchanged = unchanged && value == u.model
		case "effort":
			unchanged = unchanged && value == u.reasoningEffort
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
	if command != "/model" && command != "/reasoning" && command != "/tier" {
		return false, nil
	}
	var choices []string
	switch command {
	case "/model":
		for _, model := range u.models {
			if !model.Hidden {
				choices = append(choices, model.Model)
			}
		}
	case "/reasoning":
		choices = u.effortChoices()
	case "/tier":
		choices = []string{"default"}
		if model := u.currentModel(); model != nil {
			for _, tier := range model.Tiers {
				choices = append(choices, tier.ID)
			}
		}
	}
	if len(fields) != 2 {
		if u.modelsLoading {
			u.settingsChoices = command
			u.setNotice("Loading model choices…", false)
			return true, nil
		}
		body := command + " VALUE"
		if len(choices) == 0 {
			body += "\nNo advertised choices; explicit values are validated by Codex."
		} else {
			body += "\n" + strings.Join(choices, "\n")
		}
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Settings", Kind: "text", Text: body}}})
		u.view.follow()
		u.setNotice("Choices shown above · PgUp/PgDn scroll", false)
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
	case "/reasoning":
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
			u.modelsLoading, u.reasoningKey, u.settingsChoices = false, nil, ""
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
			u.settingsChoices = ""
			_, _ = u.settingsCommand(command)
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
			u.setNotice(method+": "+m.Error.Message, true)
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
	u.model, u.reasoningEffort, u.serviceTier = event.Settings.Model, event.Settings.Effort, event.Settings.ServiceTier
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
	}
	return true, nil
}
