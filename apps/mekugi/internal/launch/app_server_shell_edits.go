package launch

import (
	"context"
	"time"

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

func (session *nativeSession) captureShellEdit(attached *attachedPresentation, terminal *terminalsession.Attachment, parent context.Context, command string) *executionobservation.ShellOperation {
	if session.retained.Store == nil {
		return nil
	}
	ctx := session.retainedSession.Context
	if ctx == nil {
		ctx = parent
	}
	// The attachment's session lease can predate enrollment as a delegate.
	// Resolve its durable family namespace at admission, while retaining the
	// selected thread as the shell operation's author.
	var namespace string
	if err := session.retained.Store.Transaction(ctx, nil, func(*history.Transaction) error {
		var err error
		namespace, err = session.retained.Store.NamespaceForThread(session.Client.Thread)
		return err
	}); err != nil {
		setNotice(&attached.feedback, terminal, time.Now(), "Shell change capture failed: "+err.Error(), true)
		return nil
	}
	edit := executionobservation.PrepareShell(ctx, session.retained, session.changeSource, session.snapshots, session.recovered, session.publishChanges, session.execWindows, session.audience, session.captureNotice, executionobservation.ShellInput{
		Thread: session.Client.Thread, Turn: session.Client.Turn, Namespace: namespace,
		Workspace: attached.cwd, Caller: attached.session.Caller(session.Client.Thread), Command: command, Shell: session.shellEdits.shell,
	})
	if edit == nil {
		return nil
	}
	restoreDiffThread(attached, terminal, session.review, session.reviewSource, session.preparePane, binding.ThreadInfo{ID: session.Client.Thread, Cwd: attached.cwd})
	edit.Begin(session.review)
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
