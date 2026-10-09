package router

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/session"
)

type runtimeShellCapture struct {
	call       ObservationCall
	baseline   *execObservation
	window     *execWindow
	registered bool
}

func (u *appServerUI) submitRuntimeShell(command string) error {
	r := u.runtime
	if !r.ready || r.settings != nil || r.changeRequest != "" || r.resetRequest != "" || r.shell != nil || r.continuation != nil {
		u.setNotice("Wait for native session or shell completion · draft kept", false)
		return nil
	}
	if len(u.images) != 0 || len(u.skills) != 0 || len(u.files) != 0 || len(u.selections) != 0 {
		u.setNotice("Shell commands use plain text · remove attachments and picker tokens first", true)
		return nil
	}
	r.serial++
	c := session.ShellCommand{ID: fmt.Sprintf("shell/%d", r.serial), SessionID: u.thread, Command: command}
	captureErr := u.captureRuntimeShell(c)
	if err := r.client.(session.ShellClient).RunShell(u.ctx, c); err != nil {
		u.discardRuntimeShellCapture()
		u.setNotice("Shell command not sent: "+err.Error()+" · draft kept", true)
		return nil
	}
	r.shell = &c
	u.shellCommand.begin(u.takeDraft(), "")
	u.setNotice("Shell command submitted", false)
	if captureErr != nil {
		u.setNotice("Shell change capture unavailable: "+captureErr.Error(), true)
	}
	u.view.follow()
	return nil
}

func (u *appServerUI) runtimeShellEvent(e session.Event) {
	s := e.Shell
	if s == nil {
		return
	}
	r := u.runtime
	if !e.Historical {
		if r.shell == nil || r.shell.ID != s.ID || r.shell.SessionID != "" && r.shell.SessionID != s.SessionID {
			return
		}
		if e.Kind == "shell_started" {
			if capture := r.shellCapture; capture != nil {
				if capture.call.Command != s.Command || capture.registered && capture.call.Binding.Session != s.SessionID {
					u.discardRuntimeShellCapture()
					u.setNotice("Shell change capture unavailable: native execution identity changed", true)
				}
			}
			r.shell.SessionID = s.SessionID
			u.shellResponse(nil)
			u.shellCommand.started(s.ID)
			u.shellCommand.identified()
		} else {
			started := u.shellCommand.phase == shellRunning
			var captureErr error
			if capture := r.shellCapture; started && capture != nil && capture.call.Command == s.Command && s.ExitCode != nil {
				if !capture.registered {
					capture.call.Binding.Session = s.SessionID
					captureErr = r.observations.owner.beforePrepared(context.WithoutCancel(u.ctx), capture.call, capture.baseline, capture.window)
					capture.registered = captureErr == nil
				}
				status := "completed"
				if *s.ExitCode != 0 {
					status = "failed"
				}
				if captureErr == nil && capture.call.Binding.Session == s.SessionID {
					_, captureErr = r.observations.owner.after(context.WithoutCancel(u.ctx), capture.call, ObservationTerminal{Status: status, Report: s.Output})
				}
			}
			u.discardRuntimeShellCapture()
			r.shell = nil
			if !started {
				u.shellResponse(&appserver.Error{Message: e.Text})
			} else if !s.Retained {
				u.setNotice("Native shell history unavailable: "+e.Text+" · execution is not repeated", true)
			} else {
				u.setNotice("Shell command completed · context retained", false)
			}
			u.shellCommand.completed()
			if captureErr != nil {
				u.setNotice("Shell change capture failed: "+captureErr.Error(), true)
			}
			if !started {
				return
			}
		}
	}
	// Use the original command/output owners. These are direct user actions,
	// not model tool results, task identities or capture authorization.
	item := appServerItem{Type: "commandExecution", Command: s.Command, ExitCode: s.ExitCode}
	if e.Kind == "shell_done" && s.ExitCode != nil {
		item.AggregatedOutput = &s.Output
	}
	for _, v := range []*liveActivityView{u.view, u.agents} {
		n := &liveActivityNativeItem{thread: u.thread, item: s.ID, command: s.Command, running: e.Kind == "shell_started" && !e.Historical}
		for _, old := range v.entries {
			if old.CallID == s.ID && old.native != nil {
				copy := *old.native
				n = &copy
				n.running = e.Kind == "shell_started" && !e.Historical
				break
			}
		}
		if n.output == nil {
			n.output = u.session.outputs.New()
		}
		if !e.Historical && n.commandStarted.IsZero() {
			n.commandStarted = u.now()
		}
		entry := activityPaneEntry{Seq: v.lastSeq + 1, Agent: "Main", Kind: "tool", Text: appServerCommandText(item, u.session.cwd), CallID: s.ID, Observed: u.now(), native: n}
		entries := []activityPaneEntry{entry}
		if e.Kind == "shell_done" {
			if !e.Historical {
				n.commandEnded = u.now()
			}
			u.session.retainOutput(n, item)
			settled := u.now()
			if e.Historical {
				settled = time.Time{}
			}
			entries = u.session.commandDone(entry, item, settled)
		}
		for i := range entries {
			entries[i].Seq = v.lastSeq + uint64(i) + 1
		}
		v.apply(activityPaneEvent{Kind: "entries", Entries: entries})
	}
}

// Local user submission opens the shared window before native execution. Only
// matching native lifecycle evidence can settle it; history never opens one.
func (u *appServerUI) captureRuntimeShell(command session.ShellCommand) error {
	r := u.runtime
	if r.observations == nil {
		return nil
	}
	o := r.observations.owner
	o.mu.Lock()
	b := ObservationBinding{Runtime: o.runtime, Workspace: o.workspace, Session: o.session}
	o.mu.Unlock()
	if command.SessionID != "" && command.SessionID != b.Session || b.Workspace != u.session.cwd {
		return fmt.Errorf("confirmed native session scope unavailable")
	}
	call := ObservationCall{Binding: b, ID: "user-shell/" + rand.Text(), Tool: "userShell", Command: command.Command,
		Input: string(mustMarshalJSON(command)), Workdir: b.Workspace}
	capture := &runtimeShellCapture{call: call}
	if b.Session != "" {
		if err := o.before(u.ctx, call); err != nil {
			return err
		}
		capture.registered = true
	} else {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.session != "" || o.workspace != b.Workspace {
			return fmt.Errorf("native session changed before shell capture")
		}
		baseline, err := prepareNativeObservation(u.ctx, o.store, b.Workspace, call)
		if err != nil {
			return err
		}
		capture.baseline = baseline
		capture.window = &execWindow{ref: observationKey(call), roots: baseline.Roots, named: true,
			endpointScope: baseline.scopePaths(), endpointWide: call.Command != "" && baseline.Class != execNeutral.String()}
		o.windows.open(capture.window)
	}
	r.shellCapture = capture
	return nil
}

func (u *appServerUI) discardRuntimeShellCapture() {
	r := u.runtime
	if r.shellCapture == nil {
		return
	}
	if r.shellCapture.registered || r.shellCapture.window != nil {
		o := r.observations.owner
		key := observationKey(r.shellCapture.call)
		if r.shellCapture.window != nil {
			key = r.shellCapture.window.ref
		}
		o.mu.Lock()
		o.windows.close(key)
		o.settled(key, "")
		o.mu.Unlock()
	}
	r.shellCapture = nil
}
