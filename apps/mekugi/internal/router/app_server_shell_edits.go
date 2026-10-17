package router

import (
	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Only locally submitted commands own observation windows. History restoration
// never captures a new baseline or revives an unfinished command.
type appServerShellEdits struct {
	shell   string
	pending *shellObservation
	running map[[2]string]*shellObservation
}

func (u *appServerUI) captureShellEdit(command string) *shellObservation {
	if u.retained == nil || u.replay != nil {
		return nil
	}
	ctx := u.session.waitContext
	if ctx == nil {
		ctx = u.ctx
	}
	edit := prepareShellObservation(ctx, u.retained, u.execWindows, u.audience, u.captureNotice, shellObservationInput{
		Thread: u.Thread, Turn: u.Turn, Namespace: u.session.waitNamespace,
		Workspace: u.session.cwd, Caller: u.session.path(u.Thread), Command: command, Shell: u.shellEdits.shell,
	})
	if edit == nil {
		return nil
	}
	u.restoreDiffThread(binding.ThreadInfo{ID: u.Thread, Cwd: u.session.cwd}, true)
	edit.begin(u.review)
	return edit
}

// Capture completion before presentation can discard an event from a thread
// the user has left. The observation retains its original workspace and scope.
func (u *appServerUI) observeShellMessage(message appserver.Message) {
	if message.Method != "item/started" && message.Method != "item/completed" || u.shellEdits.pending == nil && len(u.shellEdits.running) == 0 {
		return
	}
	var event binding.Notification
	if json.Unmarshal(message.Params, &event) == nil {
		u.observeShellEdit(event.ThreadID, message.Method, event.Item)
	}
}

func (u *appServerUI) observeShellEdit(thread, method string, item binding.ThreadItem) {
	if item.Type != "commandExecution" || item.Source != "userShell" || item.ID == "" {
		return
	}
	key := [2]string{thread, item.ID}
	if method == "item/started" {
		edit := u.shellEdits.pending
		if edit == nil || edit.Thread() != thread {
			return
		}
		u.shellEdits.pending = nil
		if u.shellEdits.running == nil {
			u.shellEdits.running = make(map[[2]string]*shellObservation)
		}
		edit.BindItem(item.ID)
		u.shellEdits.running[key] = edit
		return
	}
	if method != "item/completed" {
		return
	}
	edit := u.shellEdits.running[key]
	if edit == nil {
		return
	}
	delete(u.shellEdits.running, key)
	output := ""
	if item.AggregatedOutput != nil {
		output = *item.AggregatedOutput
	}
	host := binding.ToolResult{CallID: item.ID, Tool: item.Source, Status: item.Status, ExitCode: item.ExitCode}
	if err := edit.Complete(host, output); err != nil {
		u.setNotice("Shell change capture failed: "+err.Error(), true)
	}
}
