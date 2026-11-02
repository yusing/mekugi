package launch

import (
	"context"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// dispatchViewResponse is the single response binding for presentation-owned
// acquisitions. The sole request ledger attributes each reply before this call.
func (session *nativeSession) dispatchViewResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, clock func() time.Time, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, quit *bool, parent context.Context, response sessioncontrol.Request[appServerRequest], m appserver.Message) (bool, error) {
	method := response.Method
	if method == "permissionProfile/list" {
		return true, picker.permissionsResponse(attached, &session.Client, response.Owner, m)
	}
	if response.Owner.label != "" {
		method = response.Owner.label
	}
	if method == "rewind/turns" {
		rewindTurnsResponse(&session.Client, picker, response.Owner, m)
		return true, nil
	}
	if method == "rewind/page" {
		rewindPageResponse(&session.Client, picker, response.Owner, m)
		return true, nil
	}
	if method == "rewind/source" {
		rewindSourceResponse(&session.Client, picker, parent, response.Owner, m)
		return true, nil
	}
	if method == resumePickerList {
		return true, session.resumePickerResponse(attached, terminal, picker, editor, clock, side, panel, resumePicker, quit, m)
	}
	if handled, err := statusMessage(session.Client.Requests, (*panel), method, m, response.Owner.status); handled {
		return true, err
	}
	if picker.pickerMessage(attached, terminal, clock, session.Client.Requests, (*editor), parent, session.skillEnvironment, method, m) {
		return true, nil
	}

	return false, nil
}
