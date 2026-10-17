package router

import (
	"context"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/contracts/scope"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

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

func captureShellEdit(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], parent context.Context, reviewOwner *sessionreview.Review, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store), command string) *executionobservation.ShellOperation {
	if retained.Store == nil {
		return nil
	}
	ctx := retainedSession.Context
	if ctx == nil {
		ctx = parent
	}
	edit := executionobservation.PrepareShell(ctx, retained, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, executionobservation.ShellInput{
		Thread: control.Thread, Turn: control.Turn, Namespace: retainedSession.Namespace,
		Workspace: attached.cwd, Caller: attached.session.Caller(control.Thread), Command: command, Shell: shellEdits.shell,
	})
	if edit == nil {
		return nil
	}
	restoreDiffThread(attached, terminal, reviewOwner, reviewSource, preparePane, binding.ThreadInfo{ID: control.Thread, Cwd: attached.cwd}, true)
	edit.Begin(reviewOwner)
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
