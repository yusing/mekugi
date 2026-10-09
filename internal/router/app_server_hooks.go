package router

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/pathdisplay"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Source: codex-rs/app-server-protocol/src/protocol/v2/hook.rs HookRunSummary.
type appServerHookRun struct {
	ID            string `json:"id"`
	EventName     string `json:"eventName"`
	HandlerType   string `json:"handlerType"`
	ExecutionMode string `json:"executionMode"`
	SourcePath    string `json:"sourcePath"`
	Status        string `json:"status"`
	StatusMessage string `json:"statusMessage"`
	StartedAt     int64  `json:"startedAt"` // Host epoch seconds, not milliseconds.
	CompletedAt   *int64 `json:"completedAt"`
	DurationMS    *int64 `json:"durationMs"`
	Entries       []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"entries"`
}

func hookOutput(run appServerHookRun) (string, bool) {
	var output activityui.Output
	if run.StatusMessage != "" {
		output.Write("status: " + run.StatusMessage + "\n")
	}
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

func (u *appServerUI) hookEvent(method string, event appServerEvent) error {
	if event.ThreadID == "" || event.Run == nil || event.Run.ID == "" {
		return fmt.Errorf("%s: missing hook thread or run identity", method)
	}
	s := &u.session
	if event.ThreadID != u.thread {
		if err := u.requestThreadMetadata(event.ThreadID); err != nil {
			return err
		}
	}
	if h := u.childHistory[event.ThreadID]; h != nil {
		h.live = true
	}
	run := *event.Run
	completed := method == "hook/completed"
	var previousOutput *activityui.Output
	view := u.agents
	if event.ThreadID == u.thread {
		view = u.view
	}
	// Use the existing item reconciliation to ignore duplicate starts and late
	// starts after completion. Hook identity is thread + run, independently of turn.
	for _, entry := range view.entries {
		if n := entry.native; n != nil && n.hook != nil && n.thread == event.ThreadID && n.item == run.ID {
			if n.phase == "item/completed" || !completed && n.phase != "hook/restored" {
				return nil
			}
			previousOutput = n.output
		}
	}
	output, hasError := hookOutput(run)
	observation := retainedHookObservation{Turn: event.TurnID, Run: run, At: u.now(), Output: output, HasError: hasError, Completed: completed}
	observation.Run.Entries, observation.Run.StatusMessage = nil, ""
	if u.replay == nil {
		if err := s.retainHook(event.ThreadID, observation); err != nil {
			u.setNotice("Hook observation could not be retained: "+err.Error(), true)
		}
	}
	entry := s.hookEntry(event.ThreadID, observation, u.replay == nil)
	if previousOutput != nil {
		previousOutput.Release()
	}
	entry.Seq = s.next()
	u.applyActivity([]activityPaneEntry{entry}, slices.Clone(s.agents))
	return nil
}

func (s *appServerSession) hookEntry(thread string, observation retainedHookObservation, live bool) activityPaneEntry {
	run := observation.Run
	hook := &activityui.HookDetails{HandlerType: run.HandlerType, ExecutionMode: run.ExecutionMode,
		Status: run.Status, HasError: observation.HasError}
	n := &liveActivityNativeItem{thread: thread, turn: observation.Turn, item: run.ID, hook: hook,
		phase: "item/completed", live: live, status: run.Status}
	if live && !observation.Completed {
		n.phase, n.running = "item/started", true
	} else if !observation.Completed {
		n.phase = "hook/restored"
	}
	if run.StartedAt > 0 {
		n.commandStarted = time.Unix(run.StartedAt, 0)
	}
	if run.CompletedAt != nil {
		n.commandEnded = time.Unix(*run.CompletedAt, 0)
	}
	if run.DurationMS != nil {
		n.duration = time.Duration(*run.DurationMS) * time.Millisecond
	}
	n.output = s.outputs.New()
	n.output.Write(observation.Output)
	if !n.running {
		n.output.Finish(nil, nil) // Hook status does not imply a shell exit.
	}
	label := run.EventName + " · " + commentaryCode(pathdisplay.ForWorkspace(s.cwd, run.SourcePath))
	if run.ExecutionMode == "async" {
		label += " · async"
	}
	if !observation.Completed && !live {
		label += " · outcome unknown"
	}
	entry := activityPaneEntry{Agent: s.path(thread), Kind: "hook", CallID: run.ID, Text: label, Observed: observation.At, native: n}
	entry.outputTail, entry.outputOmit = appServerOutputTail(&observation.Output)
	if observation.Completed && run.Status == "completed" && !observation.HasError {
		n.collapsed = !live
		if live && len(entry.outputTail) > 0 {
			n.settled = observation.At
		}
	}
	return entry
}
