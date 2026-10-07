package router

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/internal/session"
)

func (u *appServerUI) runtimeSessionPage(e session.Event) error {
	p := u.resumePicker
	if p == nil || e.Sessions == nil || p.request != e.Sessions.ID {
		return nil
	}
	p.request, p.loading = "", false
	if e.Failed {
		u.resumePickerFailed("Could not list sessions: " + e.Text)
		return nil
	}
	var rows []appServerResumeRow
	for _, saved := range e.Sessions.Sessions {
		rows = append(rows, appServerResumeRow{id: saved.ID, title: strings.TrimSpace(saved.Title), cwd: saved.Cwd, branch: saved.Branch, updated: saved.Updated})
	}
	return u.receiveResumePage(rows, e.Sessions.Cursor)
}

func (u *appServerUI) changeRuntimeSession(target, workspace string) error {
	r := u.runtime
	client, ok := r.client.(session.SessionChangeClient)
	if !ok {
		u.setNotice("Session switching is unavailable for this runtime", true)
		return nil
	}
	if u.sessionBusy() {
		u.setNotice("Session switching waits for idle native work, permissions and settings", true)
		return nil
	}
	if err := u.cancelRuntimeContinuation(false); err != nil {
		return err
	}
	r.serial++
	id := fmt.Sprintf("session/%d", r.serial)
	if err := client.ChangeSession(u.ctx, session.SessionChange{ID: id, SessionID: target, Cwd: workspace}); err != nil {
		u.setNotice("Session switch not sent: "+err.Error()+" · draft kept", true)
		return nil
	}
	u.takeDraft()
	if target == "" {
		u.replacement.clear()
	} else {
		u.replacement.resume(target)
	}
	r.changeRequest, r.ready = id, false
	u.status = "Changing native session…"
	return nil
}

func (u *appServerUI) runtimeSessionChanged(e session.Event) error {
	r := u.runtime
	if e.Change == nil || e.Change.ID != r.changeRequest || r.changeRequest == "" {
		return nil
	}
	if e.Kind == "session_ready" {
		r.changeRequest, r.ready = "", true
		u.replacement.finish()
		u.status = "Ready"
		if e.Failed {
			u.setNotice("Could not change session: "+e.Text, true)
		}
		return nil
	}
	if err := u.clearSessionPresentation(); err != nil {
		return err
	}
	u.leaveThread()
	if e.Change.Cwd != "" {
		u.session.cwd = e.Change.Cwd
		u.shell.diff.workspace = e.Change.Cwd
	}
	u.resetDiffScope()
	u.pendingTitle = ""
	u.thread, u.title = "", e.Change.Title
	u.releaseRetired(e.Change.SessionID)
	*r = nativeRuntimeSession{client: r.client, name: r.name, serial: r.serial, observations: r.observations,
		commandInfo: r.commandInfo, models: r.models, changeRequest: r.changeRequest}
	if r.observations != nil {
		u.attachRuntimeObservation(r.observations)
	}
	if e.Change.SessionID != "" {
		if err := u.runtimeEvent(session.Event{Kind: "session", SessionID: e.Change.SessionID}); err != nil {
			return err
		}
	}
	u.runtimeRoster()
	return nil
}
