package router

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/session"
)

func (u *appServerUI) runtimeModel() *session.Model {
	for i := range u.runtime.models {
		m := &u.runtime.models[i]
		if m.ID == u.model {
			return m
		}
	}
	for i := range u.runtime.models {
		m := &u.runtime.models[i]
		if m.ID != "default" && m.Resolved != "" && m.Resolved == u.model {
			return m
		}
	}
	return nil
}

func (u *appServerUI) runtimeSettingsCommand(text string) (bool, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 || fields[0] != "/model" && fields[0] != "/effort" && fields[0] != "/reasoning" {
		return false, nil
	}
	if _, ok := u.runtime.client.(session.SettingsClient); !ok || !u.runtime.ready {
		u.setNotice("Native settings controls unavailable · draft kept", true)
		return true, nil
	}
	if len(fields) > 2 {
		u.setNotice("Use "+fields[0]+" or "+fields[0]+" VALUE", true)
		return true, nil
	}
	if len(fields) == 1 {
		u.deleteDraftRange(0, len(u.draft))
		u.runtimeSettingsPicker(fields[0])
		return true, nil
	}
	field := "model"
	if fields[0] != "/model" {
		field = "effort"
	}
	accepted, err := u.runtimeUpdateSettings(field, fields[1])
	if accepted {
		u.recordDraft()
		u.draft, u.cursorBack = "", 0
	}
	return true, err
}

// Opening via a shortcut must preserve the user's ordinary composer draft.
func (u *appServerUI) runtimeSettingsPicker(command string) {
	if _, ok := u.runtime.client.(session.SettingsClient); !ok || !u.runtime.ready {
		u.setNotice("Native settings controls unavailable · draft kept", true)
		return
	}
	choices := []string{"default"}
	if command == "/model" {
		for _, m := range u.runtime.models {
			if !slices.Contains(choices, m.ID) {
				choices = append(choices, m.ID)
			}
		}
	} else {
		choices = append(choices, u.effortChoices()...)
	}
	u.showSettingsPicker(command, choices)
}

func (u *appServerUI) runtimeUpdateSettings(field, value string) (bool, error) {
	r := u.runtime
	client, ok := r.client.(session.SettingsClient)
	if !ok || !r.ready {
		u.setNotice("Native settings controls unavailable", true)
		return false, nil
	}
	if r.settings != nil {
		u.setNotice("Settings update pending", false)
		return false, nil
	}
	if field != "model" && field != "effort" {
		u.setNotice("This settings control is not supported by the runtime", true)
		return false, nil
	}
	if field == "effort" && value != "default" && !slices.Contains(u.effortChoices(), value) {
		u.setNotice("Effort is not advertised for the current native model", true)
		return false, nil
	}
	r.serial++
	s := session.Settings{ID: fmt.Sprintf("settings/%d", r.serial), Field: field, Value: value}
	if err := client.SetSettings(u.ctx, s); err != nil {
		return false, err
	}
	r.settings = &s
	u.setNotice("Updating native "+field+"…", false)
	return true, nil
}

func (u *appServerUI) runtimeSettingsReceipt(e session.Event) {
	r := u.runtime
	if e.Settings == nil || r.settings == nil || *e.Settings != *r.settings {
		return
	}
	s := *r.settings
	r.settings = nil
	if e.Failed {
		u.setNotice("Native "+s.Field+" update failed: "+e.Text, true)
		return
	}
	if s.Field == "model" {
		// The native setter acknowledged the selection. The next init event
		// supplies the resolved model, including native fallback.
		u.model = s.Value
		u.setNotice("Native model selection accepted", false)
	} else {
		r.effortRequest = s.Value
		if s.Value == "default" {
			r.effortRequest = ""
		}
		u.setNotice("Effort override accepted for next turn · native policy may limit it", false)
	}
}
