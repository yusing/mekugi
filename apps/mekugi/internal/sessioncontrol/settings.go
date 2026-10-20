package sessioncontrol

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"maps"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

type settingsPhase uint8

const (
	settingsIdle settingsPhase = iota
	settingsThread
	settingsTurn
)

type Settings struct {
	phase         settingsPhase
	change        map[string]any
	turn          string
	RestoreEffort bool
}

type SettingsAction uint8

const (
	SettingsUnavailable SettingsAction = iota
	SettingsAlreadyPending
	SettingsUnchanged
	SettingsThread
	SettingsTurn
)

type SettingsPlan struct {
	Action                     SettingsAction
	Change                     map[string]any
	Turn                       string
	ChooseTier, RestoreDefault bool
}

// PrepareSettings decides admission without changing operation state. The caller
// commits the plan only after its request was successfully issued.
// Source: apps/mekugi/internal/router/app_server_settings.go:53:113@[59a0d2b9]
func (h *Live) PrepareSettings(current preferences.Applied, change map[string]any, restoring bool) SettingsPlan {
	if restoring || !h.AcceptsSettings() {
		return SettingsPlan{Action: SettingsUnavailable}
	}
	if h.Settings.Pending() {
		return SettingsPlan{Action: SettingsAlreadyPending}
	}
	plan := SettingsPlan{Action: SettingsThread, Change: maps.Clone(change), Turn: h.Turn}
	for field, value := range change {
		switch field {
		case "collaborationMode":
			return plan // A complete host snapshot needs authoritative confirmation.
		case "model":
			if value != current.Model {
				return plan
			}
		case "effort":
			if value != current.Effort && !(value == nil && current.Effort == "") {
				return plan
			}
		case "serviceTier":
			// Codex normalizes the legacy fast spelling to priority.
			// Source: codex-rs/core/src/session/step_settings.rs:274:283@86be5320.
			if value != current.Tier && !(value == "fast" && current.Tier == "priority") && !(value == nil && (current.Tier == "" || current.Tier == "default")) {
				return plan
			}
		}
	}
	_, plan.ChooseTier = change["serviceTier"]
	if h.Turn != "" {
		plan.Action = SettingsTurn
	} else {
		plan.Action = SettingsUnchanged
		_, explicit := change["effort"]
		plan.RestoreDefault = explicit && h.Settings.RestoreEffort
	}
	return plan
}

func (o *Settings) Pending() bool     { return o.phase != settingsIdle }
func (o *Settings) BlocksInput() bool { return o.Pending() || o.RestoreEffort }

func (o *Settings) Admit(plan SettingsPlan) {
	switch plan.Action {
	case SettingsThread:
		o.phase, o.change, o.turn = settingsThread, plan.Change, plan.Turn
	case SettingsTurn:
		o.phase = settingsTurn
	case SettingsUnchanged:
		if plan.RestoreDefault {
			o.RestoredEffort()
		}
	}
}

func (o *Settings) Finish()         { o.phase, o.change, o.turn = settingsIdle, nil, "" }
func (o *Settings) RestoredEffort() { o.RestoreEffort = false }

type SettingsConfirmation struct {
	Requested, ChooseTier bool
	Update                SettingsPlan
}

// ApplySettings admits an already prepared plan only after successful dispatch.
// It shares Main's request scope, including its retirement on replacement.
// Source: apps/mekugi/internal/router/app_server_settings.go:56:95,331:340@a4869fd4.
func (r *Requests[T]) ApplySettings(control *Live, plan SettingsPlan) error {
	switch plan.Action {
	case SettingsThread, SettingsTurn:
		params := make(map[string]any, len(plan.Change)+2)
		maps.Copy(params, plan.Change)
		params["threadId"] = control.Thread
		method := "thread/settings/update"
		if plan.Action == SettingsTurn {
			method, params["turnId"] = "turn/settings/update", plan.Turn
		}
		var owner T
		if _, err := r.Send(method, params, "main", owner); err != nil {
			return err
		}
	case SettingsUnchanged:
	default:
		return nil
	}
	control.Settings.Admit(plan)
	return nil
}

type SettingsAcknowledgement struct {
	Failed, RestoreWaiting bool
	Status                 string
}

// Acknowledge settles only the correlated RPC. A successful thread update waits
// for the authoritative settings notification before releasing dependent input.
// Source: apps/mekugi/internal/router/app_server_settings.go:250:284@a4869fd4.
func (o *Settings) Acknowledge(method string, result jsontext.Value, failed bool) (SettingsAcknowledgement, error) {
	ack := SettingsAcknowledgement{Failed: failed}
	if failed {
		o.Finish()
		ack.RestoreWaiting = o.RestoreEffort
	} else if method == "turn/settings/update" {
		var response struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(result, &response); err != nil {
			return ack, err
		}
		o.Finish()
		ack.Status = response.Status
	}
	return ack, nil
}

// Confirmed consumes authoritative settings. A live update can target only the
// turn captured by the original intent, never a later turn.
func (o *Settings) Confirmed(turn, effort string) SettingsConfirmation {
	var result SettingsConfirmation
	_, result.ChooseTier = o.change["serviceTier"]
	if _, explicit := o.change["effort"]; explicit || effort == "" {
		o.RestoredEffort()
	}
	if !o.Pending() || o.change == nil {
		return result
	}
	result.Requested = true
	result.Update = SettingsPlan{Action: SettingsUnchanged, Change: o.change}
	o.change = nil
	if o.turn != "" && o.turn == turn {
		result.Update.Action, result.Update.Turn = SettingsTurn, o.turn
	} else {
		o.Finish()
	}
	return result
}
