package router

import (
	"fmt"

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

func (u *appServerUI) guardHookResponse(message appserver.Message) error {
	thread := u.guardHookCheck.pendingThread
	u.guardHookCheck.pendingThread = ""
	if thread != u.Thread {
		return u.flushInput()
	}
	var err error
	if message.Error != nil {
		err = fmt.Errorf("read approval hook: %s", message.Error.Message)
	} else {
		err = shellhook.Validate(message.Result, u.attached.cwd, u.guardHookCheck.hash)
	}
	if err != nil {
		u.restoreDrafts(append(u.attached.conversation.Unsent, u.attached.conversation.Queued...)...)
		u.attached.conversation.Unsent, u.attached.conversation.Queued = nil, nil
		u.attached.presentation.Status.SetText("Ready")
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), "Shell guard: "+err.Error(), true)
		return nil
	}
	u.guardHookCheck.readyThread = thread
	return u.flushInput()
}
