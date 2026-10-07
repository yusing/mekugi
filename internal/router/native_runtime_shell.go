package router

import (
	"fmt"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/session"
)

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
	if err := r.client.(session.ShellClient).RunShell(u.ctx, c); err != nil {
		u.setNotice("Shell command not sent: "+err.Error()+" · draft kept", true)
		return nil
	}
	r.shell = &c
	u.shellCommand.begin(u.takeDraft(), "")
	u.setNotice("Shell command submitted", false)
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
			r.shell.SessionID = s.SessionID
			u.shellResponse(nil)
			u.shellCommand.started(s.ID)
			u.shellCommand.identified()
		} else {
			started := u.shellCommand.phase == shellRunning
			r.shell = nil
			if !started {
				u.shellResponse(&appserver.Error{Message: e.Text})
			} else if !s.Retained {
				u.setNotice("Native shell history unavailable: "+e.Text+" · execution is not repeated", true)
			} else {
				u.setNotice("Shell command completed · context retained", false)
			}
			u.shellCommand.completed()
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
