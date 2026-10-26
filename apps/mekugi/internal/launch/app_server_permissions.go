package launch

import (
	json "encoding/json/v2"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func (p *composerPicker) cancelPermissionSelection() {
	if p.permissionSelection != nil {
		p.permissionSelection.Closed = true
		p.permissionSelection = nil
	}
}

func (p *composerPicker) requestPermissions(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], cursor string) error {
	params := map[string]any{"cwd": attached.cwd}
	if cursor != "" {
		params["cursor"] = cursor
	}
	_, err := control.Requests.Send("permissionProfile/list", params, viewRequestScope(attached.view), appServerRequest{view: attached.view, menu: p.ID})
	return err
}

func (p *composerPicker) permissionsResponse(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], request appServerRequest, message appserver.Message) error {
	selection := p.permissionSelection
	if selection == nil || selection.Closed || !p.Open || p.ID != request.menu || p.settingThread != control.Thread {
		return nil
	}
	if message.Error != nil {
		p.Loading, p.Problem = false, "Permission choices unavailable: "+message.Error.Message
		return nil
	}
	var page struct {
		Data       []sessioncontrol.PermissionProfile `json:"data"`
		NextCursor string                             `json:"nextCursor"`
	}
	if err := json.Unmarshal(message.Result, &page); err != nil {
		p.Loading, p.Problem = false, "Permission choices unavailable: "+err.Error()
		return nil
	}
	selection.Profiles = append(selection.Profiles, page.Data...)
	if page.NextCursor != "" {
		if err := p.requestPermissions(attached, control, page.NextCursor); err != nil {
			p.Loading, p.Problem = false, "Permission choices unavailable: "+err.Error()
		}
		return nil
	}
	p.Loading, p.Choices, p.Problem = false, nil, ""
	for _, profile := range selection.Profiles {
		choice := completion.Choice{Name: profile.ID, Description: profile.Description}
		if !profile.Allowed {
			choice.Description = "Unavailable under current requirements"
		}
		if active := control.Settings.Permissions.Active; active != nil && profile.ID == active.ID {
			choice.Description = "Current · " + choice.Description
			p.Preferred = choice.Identity()
		}
		p.Choices = append(p.Choices, choice)
	}
	if len(p.Choices) == 0 {
		p.Problem = "No permission profiles advertised · Esc to close"
	}
	return nil
}

func choosePermission(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], applied preferences.Applied, tiers **inferencegateway.Tiers, panes *nativePreferences, restoring bool, clock func() time.Time, picker *composerPicker, choice completion.Choice) {
	selection := picker.permissionSelection
	change, err := selection.Choose(choice.Name)
	if err != nil {
		picker.Problem = err.Error()
		return
	}
	selection.Closed = true // Transfer the input gate to the admitted host update.
	accepted, err := updateSettings(attached, terminal, control, applied, tiers, panes, restoring, clock, change)
	if err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Permission update failed: "+err.Error(), true)
	}
	if accepted {
		picker.cancelPermissionSelection()
		picker.Kind, picker.Open, picker.Choices, picker.Setting = "", false, nil, ""
	} else {
		selection.Closed = false
		picker.Problem = attached.feedback.Text
	}
}
