package launch

import (
	"fmt"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/contracts/scope"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/binding"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/libraries/pathdisplay"
)

// Codex's synthetic path for the invocation's -c layer, which holds Mekugi's
// own hooks and any hook flags the user passes.
// Source: codex-rs/hooks/src/engine/discovery.rs synthetic_layer_path.
const builtinHookSource = "/<session-flags>/config.toml"

func hookOutput(run appserver.HookRun) (string, bool) {
	var output activitymodel.Output
	hasError := false
	for _, entry := range run.Entries {
		hasError = hasError || entry.Kind == "error"
		output.Write(entry.Kind + ": " + entry.Text + "\n")
	}
	output.Finish(nil, nil)
	view := output.View()
	text := strings.Join(view.Lines, "\n")
	if view.Dropped > 0 {
		text = fmt.Sprintf("… %d earlier lines not retained\n", view.Dropped) + text
	}
	return text, hasError
}

func hookEvent(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], childKey *scope.Key[*appServerChildHistory], activity *activitymodel.Service, reviewOwner *sessionreview.Review, approvals *nativeApprovalDock, receipts *[]*changepresentation.Receipt, retainedSession history.Session, clock func() time.Time, method string, event binding.Notification) error {
	if event.ThreadID == "" || event.Run == nil || event.Run.ID == "" {
		return fmt.Errorf("%s: missing hook thread or run identity", method)
	}
	s := &attached.session
	if event.ThreadID != control.Thread {
		if err := requestThreadMetadata(control.Requests, &attached.session, event.ThreadID); err != nil {
			return err
		}
	}
	if h := childHistory(&attached.session, childKey, event.ThreadID); h != nil {
		h.state.ObserveLive()
	}
	run := *event.Run
	completed := method == "hook/completed"
	var previousOutput *activitymodel.Output
	view := attached.childSource
	if event.ThreadID == control.Thread {
		view = attached.source
	}
	// Use the existing item reconciliation to ignore duplicate starts and late
	// starts after completion. Hook identity is thread + run, independently of turn.
	for _, entry := range view.Entries {
		if n := entry.Source.Native; n != nil && n.Hook != nil && n.Thread == event.ThreadID && n.Item == run.ID {
			if n.Phase == "item/completed" || !completed && n.Phase != "hook/restored" {
				return nil
			}
			previousOutput = n.Output
		}
	}
	output, hasError := hookOutput(run)
	observation := history.HookObservation{Turn: event.TurnID, Run: history.HookRun(run), At: presentationTime(clock), Output: output, HasError: hasError, Completed: completed}
	observation.Run.Entries = nil
	if err := retainHook(retainedSession, attached.cwd, event.ThreadID, observation); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Hook observation could not be retained: "+err.Error(), true)
	}
	entry := hookEntry(&attached.session, activity, attached.cwd, event.ThreadID, observation, true)
	if previousOutput != nil {
		previousOutput.Release()
	}
	entry.Seq = s.Activity().NextSequence()
	applyActivity(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, []sessionpresentation.ActivityEntry{entry}, s.Roster())
	return nil
}

func hookEntry(s *sessionpresentation.Attachment, activity *activitymodel.Service, cwd, thread string, observation history.HookObservation, live bool) sessionpresentation.ActivityEntry {
	run := observation.Run
	hook := &activitypresentation.HookDetails{HandlerType: run.HandlerType, ExecutionMode: run.ExecutionMode,
		Status: run.Status, HasError: observation.HasError}
	n := &sessionpresentation.NativeItem{Thread: thread, Turn: observation.Turn, Item: run.ID, Hook: hook,
		Phase: "item/completed", Live: live, Status: run.Status}
	if live && !observation.Completed {
		n.Phase, n.Running = "item/started", true
	} else if !observation.Completed {
		n.Phase = "hook/restored"
	}
	if run.StartedAt > 0 {
		n.CommandStarted = time.Unix(run.StartedAt, 0)
	}
	if run.CompletedAt != nil {
		n.CommandEnded = time.Unix(*run.CompletedAt, 0)
	}
	if run.DurationMS != nil {
		n.Duration = time.Duration(*run.DurationMS) * time.Millisecond
	}
	if outputs := activity.Outputs(s.Scope()); outputs != nil {
		n.Output = outputs.New()
		n.Output.Write(observation.Output)
		if !n.Running {
			n.Output.Finish(nil, nil) // Hook status does not imply a shell exit.
		}
	}
	label := run.EventName
	if run.StatusMessage != "" { // The host's description of what the hook does.
		label += " · " + run.StatusMessage
	}
	if run.SourcePath == builtinHookSource {
		label += " · builtin"
	} else {
		label += " · " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(cwd, run.SourcePath))
	}
	if run.ExecutionMode == "async" {
		label += " · async"
	}
	if !observation.Completed && !live {
		label += " · outcome unknown"
	}
	entry := sessionpresentation.ActivityEntry{Agent: s.Caller(thread), Kind: "hook", CallID: run.ID, Text: label, Observed: observation.At, Native: n}
	entry.OutputTail, entry.OutputOmit = sessionpresentation.OutputTail(&observation.Output)
	if observation.Completed && run.Status == "completed" && !observation.HasError {
		if live && len(entry.OutputTail) > 0 {
			entry.Completed = observation.At
		}
	}
	return entry
}
