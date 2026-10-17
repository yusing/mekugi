package router

import (
	"fmt"

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

func (u *appServerUI) checkGuardHook() (bool, error) {
	check := &u.guardHookCheck
	if check.hash == "" || u.Turn != "" {
		return true, nil
	}
	if check.readyThread == u.Thread {
		return true, nil
	}
	if check.pendingThread == "" {
		check.pendingThread = u.Thread
		u.sessionPresentation().Status.SetText("Checking approval guard…")
		if _, err := u.Requests.Send("hooks/list", map[string]any{"cwds": []string{u.cwd}}, "main", appServerRequest{label: "approval/hooks"}); err != nil {
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
		err = shellhook.Validate(message.Result, u.cwd, u.guardHookCheck.hash)
	}
	if err != nil {
		u.restoreDrafts(append(u.conversation().Unsent, u.conversation().Queued...)...)
		u.conversation().Unsent, u.conversation().Queued = nil, nil
		u.sessionPresentation().Status.SetText("Ready")
		u.setNotice("Shell guard: "+err.Error(), true)
		return nil
	}
	u.guardHookCheck.readyThread = thread
	return u.flushInput()
}
