package launch

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
	return check.checkThread(control.Requests, control.Thread, attached.cwd, func() { attached.presentation.Status.SetText("Checking approval guard…") })
}

func (check *approvalHookCheck) checkThread(requests *sessioncontrol.Requests[appServerRequest], thread, cwd string, waiting func()) (bool, error) {
	if check.hash == "" || check.readyThread == thread {
		return true, nil
	}
	if check.pendingThread == "" {
		check.pendingThread = thread
		waiting()
		if _, err := requests.Send("hooks/list", map[string]any{"cwds": []string{cwd}}, "", appServerRequest{label: "approval/hooks"}); err != nil {
			check.pendingThread = ""
			return false, err
		}
	}
	return false, nil
}

func guardHookResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, restoring bool, owned *map[string]bool, environment []string, hook *approvalHookCheck, observations *sessioncontrol.Observations, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, message appserver.Message) error {
	thread := hook.pendingThread
	hook.pendingThread = ""
	side := control.Requests.Side(thread)
	if thread != control.Thread && !control.Requests.WorkSide(thread) {
		return flushInput(attached, terminal, control, picker, questions, approvals, clock, settings.Model, tiers, environment, (*owned), hook, editor, parent, restoring, observations, reviewOwner, receipts)
	}
	var err error
	if message.Error != nil {
		err = fmt.Errorf("read approval hook: %s", message.Error.Message)
	} else {
		err = shellhook.Validate(message.Result, attached.cwd, hook.hash)
	}
	if side != nil && side.OneOff {
		if err != nil {
			if thread == side.Thread && len(side.Drafts.Unsent) == 0 {
				side.Busy, side.Starting = false, false
				restoreSideDraft(attached, side, side.Fail("Shell guard: "+err.Error(), side.Closed))
			}
			kept := side.Drafts.Unsent[:0]
			for _, input := range side.Drafts.Unsent {
				target := side.Thread
				if input.Question != nil {
					target = input.Question.Thread
				}
				if target != thread {
					kept = append(kept, input)
					continue
				}
				if input.Question != nil {
					input.Question.Reject()
				}
				restoreSideDraft(attached, side, input)
			}
			side.Drafts.Unsent = kept
			setNotice(&attached.feedback, terminal, presentationTime(clock), "Shell guard: "+err.Error(), true)
			return nil
		}
		hook.readyThread = thread
		return nil
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
