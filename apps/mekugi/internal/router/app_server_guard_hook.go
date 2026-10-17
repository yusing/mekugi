package router

import (
	"context"
	"fmt"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/backends/codex/shellhook"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Check the host's effective hook configuration before each new user turn.
// Hook rewrites compete rather than compose, so another synchronous shell
// hook could replace the guard's instrumentation. Keep that conflict visible.
type approvalHookCheck struct {
	hash                       string
	pendingThread, readyThread string
}

func (check *approvalHookCheck) check(control *sessioncontrol.Client[appServerRequest], attached *attachedPresentation) (bool, error) {
	if check.hash == "" || control.Turn != "" {
		return true, nil
	}
	if check.readyThread == control.Thread {
		return true, nil
	}
	if check.pendingThread == "" {
		check.pendingThread = control.Thread
		attached.presentation.Status.SetText("Checking approval guard…")
		if _, err := control.Requests.Send("hooks/list", map[string]any{"cwds": []string{attached.cwd}}, "main", appServerRequest{label: "approval/hooks"}); err != nil {
			check.pendingThread = ""
			return false, err
		}
	}
	return false, nil
}

func guardHookResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, restoring bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, message appserver.Message) error {
	thread := hook.pendingThread
	hook.pendingThread = ""
	if thread != control.Thread {
		return flushInput(attached, terminal, control, picker, questions, approvals, clock, settings.Model, tiers, environment, (*owned), hook, editor, parent, restoring, observations, reviewOwner, receipts)
	}
	var err error
	if message.Error != nil {
		err = fmt.Errorf("read approval hook: %s", message.Error.Message)
	} else {
		err = shellhook.Validate(message.Result, attached.cwd, hook.hash)
	}
	if err != nil {
		restoreDrafts(attached, terminal, questions, approvals, picker, control.Turn, clock, append(attached.conversation.Unsent, attached.conversation.Queued...)...)
		attached.conversation.Unsent, attached.conversation.Queued = nil, nil
		attached.presentation.Status.SetText("Ready")
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Shell guard: "+err.Error(), true)
		return nil
	}
	hook.readyThread = thread
	return flushInput(attached, terminal, control, picker, questions, approvals, clock, settings.Model, tiers, environment, (*owned), hook, editor, parent, restoring, observations, reviewOwner, receipts)
}
