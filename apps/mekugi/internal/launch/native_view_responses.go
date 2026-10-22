package launch

import (
	"context"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// dispatchViewResponse is the single response binding for presentation-owned
// acquisitions. The sole request ledger attributes each reply before this call.
func dispatchViewResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, childKey *scope.Key[*appServerChildHistory], settings preferences.Applied, tiers *inferencegateway.Tiers, title, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, approvalMode bool, quit *bool, parent context.Context, environment []string, response sessioncontrol.Request[appServerRequest], m appserver.Message) (bool, error) {
	method := response.Method
	if response.Owner.label != "" {
		method = response.Owner.label
	}
	if method == "rewind/turns" {
		rewindTurnsResponse(control, picker, response.Owner, m)
		return true, nil
	}
	if method == "rewind/source" {
		rewindSourceResponse(control, picker, parent, response.Owner, m)
		return true, nil
	}
	if method == resumePickerList {
		return true, resumePickerResponse(attached, terminal, control, picker, editor, questions, approvals, clock, childKey, settings, tiers, title, backend, restoring, side, panel, resumePicker, approvalMode, quit, m)
	}
	if handled, err := statusMessage(control.Requests, (*panel), method, m, response.Owner.status); handled {
		return true, err
	}
	if picker.pickerMessage(attached, terminal, clock, control.Requests, (*editor), parent, environment, method, m) {
		return true, nil
	}

	return false, nil
}
