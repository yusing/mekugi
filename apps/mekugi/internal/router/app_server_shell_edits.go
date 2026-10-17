package router

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Only locally submitted commands own observation windows. History restoration
// never captures a new baseline or revives an unfinished command.
type appServerShellEdits struct {
	shell   string
	pending *executionobservation.ShellOperation
	running map[[2]string]*executionobservation.ShellOperation
}

func (u *appServerUI) captureShellEdit(command string) *executionobservation.ShellOperation {
	if u.retained.Store == nil {
		return nil
	}
	ctx := u.retainedSession.Context
	if ctx == nil {
		ctx = u.ctx
	}
	edit := executionobservation.PrepareShell(ctx, u.retained, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, executionobservation.ShellInput{
		Thread: u.Thread, Turn: u.Turn, Namespace: u.retainedSession.Namespace,
		Workspace: u.attached.cwd, Caller: u.attached.session.Caller(u.Thread), Command: command, Shell: u.shellEdits.shell,
	})
	if edit == nil {
		return nil
	}
	restoreDiffThread(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, binding.ThreadInfo{ID: u.Thread, Cwd: u.attached.cwd}, true)
	edit.Begin(u.review)
	return edit
}

// Capture completion before presentation can discard an event from a thread
// the user has left. The observation retains its original workspace and scope.
func observeShellMessage(edits *appServerShellEdits, attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, message appserver.Message) {
	if message.Method != "item/started" && message.Method != "item/completed" || edits.pending == nil && len(edits.running) == 0 {
		return
	}
	var event binding.Notification
	if json.Unmarshal(message.Params, &event) == nil {
		observeShellEdit(edits, attached, terminal, clock, event.ThreadID, message.Method, event.Item)
	}
}

func observeShellEdit(edits *appServerShellEdits, attached *attachedPresentation, terminal *terminalsession.Attachment, clock func() time.Time, thread, method string, item binding.ThreadItem) {
	if item.Type != "commandExecution" || item.Source != "userShell" || item.ID == "" {
		return
	}
	key := [2]string{thread, item.ID}
	if method == "item/started" {
		edit := edits.pending
		if edit == nil || edit.Thread() != thread {
			return
		}
		edits.pending = nil
		if edits.running == nil {
			edits.running = make(map[[2]string]*executionobservation.ShellOperation)
		}
		edit.BindItem(item.ID)
		edits.running[key] = edit
		return
	}
	if method != "item/completed" {
		return
	}
	edit := edits.running[key]
	if edit == nil {
		return
	}
	delete(edits.running, key)
	output := ""
	if item.AggregatedOutput != nil {
		output = *item.AggregatedOutput
	}
	host := binding.ToolResult{CallID: item.ID, Tool: item.Source, Status: item.Status, ExitCode: item.ExitCode}
	if err := edit.Complete(host, output); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Shell change capture failed: "+err.Error(), true)
	}
}
