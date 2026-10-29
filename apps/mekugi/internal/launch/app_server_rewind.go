package launch

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/commandargs"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/completion"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func showRewindPicker(control *sessioncontrol.Client[appServerRequest], picker *composerPicker, provider string, approvals bool, review *sessionreview.Review) {
	picker.cancelScan()
	picker.openMenu("rewind")
	picker.Target, picker.Choices, picker.Problem, picker.Loading = completion.Target{}, nil, "", true
	picker.rewindThread, picker.rewindProvider, picker.rewindApprovals = control.Thread, provider, approvals
	picker.rewind = &rewindSelection{problem: "saved change evidence is unavailable"}
	if review != nil && review.Projection.Retained.Store != nil && review.Projection.Data != nil && review.Failure == "" && !review.Restoring() {
		picker.rewind.snapshot, picker.rewind.problem = review.Projection.Data, ""
		picker.rewind.scope = review.Projection.Scope
	}
	if _, err := control.Requests.Send("thread/read", map[string]any{"threadId": control.Thread, "includeTurns": true}, viewRequestScope(picker.view), appServerRequest{label: "rewind/turns", metadata: control.Thread, menu: picker.ID, view: picker.view}); err != nil {
		picker.Loading, picker.Problem = false, "Could not load user turns: "+err.Error()
	}
}

func rewindTurnsResponse(control *sessioncontrol.Client[appServerRequest], picker *composerPicker, request appServerRequest, message appserver.Message) {
	if !picker.Open || picker.Kind != "rewind" || picker.ID != request.menu || picker.rewindThread != request.metadata || control.Thread != request.metadata {
		return
	}
	picker.Loading = false
	if message.Error != nil {
		picker.Problem = "Could not load user turns: " + message.Error.Message
		return
	}
	thread, err := binding.DecodeHistoryThread(message.Result, request.metadata)
	if err != nil {
		picker.Problem = "Could not load user turns: " + err.Error()
		return
	}
	picker.rewind.sources = []binding.ThreadInfo{{ID: thread.ID, Path: thread.Path}}
	for _, turn := range thread.Turns {
		if turn.ID == "" || turn.Status == "inProgress" {
			continue
		}
		for _, item := range turn.Items {
			if item.Type != "userMessage" {
				continue
			}
			text, _, _ := conversationbinding.UserText(item.Content)
			if answer, _, replied := conversationbinding.ReplyText(text); replied {
				text = answer
			}
			text = strings.Join(strings.Fields(text), " ")
			picker.Choices = append(picker.Choices, completion.Choice{Name: turn.ID, Display: cmp.Or(text, "User turn (no text)"), Description: "Fork immediately before this turn"})
			break
		}
	}
	if len(picker.Choices) == 0 {
		picker.Problem = "No user turns available to rewind"
	}
}

func chooseRewind(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, applied preferences.Applied, restoring bool, clock func() time.Time, choice completion.Choice, parent context.Context) {
	if restoring || control.Replacement.Pending() || control.Thread != picker.rewindThread || sessionBusy(&control.Live, attached.conversation) {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the session to be idle before rewinding", true)
		return
	}
	if picker.Kind == "rewind" {
		picker.rewind.turn = choice.Name
		if picker.rewind.problem == "" {
			hasChanges := false
			for attempt := range picker.rewind.snapshot.Attempts() {
				if attempt.HasAuthoredChanges() {
					hasChanges = true
					break
				}
			}
			if !hasChanges {
				forkChosenRewind(attached, terminal, control, picker, applied, clock)
				return
			}
		}
		picker.openMenu("rewind-edits")
		picker.Choices = []completion.Choice{{ID: 1, Name: "Keep edits"}}
		if picker.rewind.problem == "" {
			picker.Loading = true
			for attempt := range picker.rewind.snapshot.Attempts() {
				if !attempt.HasAuthoredChanges() {
					continue
				}
				if attempt.Operation.Thread == "" {
					picker.rewind.problem, picker.Loading = "captured change has no host thread identity", false
					return
				}
				if attempt.Operation.Thread != control.Thread && !slices.Contains(picker.rewind.pending, attempt.Operation.Thread) {
					picker.rewind.pending = append(picker.rewind.pending, attempt.Operation.Thread)
				}
			}
			picker.readRewindSource(control, parent)
		}
		return
	}
	if choice.ID == 2 && picker.rewind.command != "" {
		picker.cancelScan()
		control.Rewind = &sessioncontrol.RewindOperation{Thread: control.Thread, BeforeTurn: picker.rewind.turn, Provider: picker.rewindProvider, Approvals: picker.rewindApprovals, Settings: applied, Command: picker.rewind.command}
		picker.Kind, picker.Open, picker.Choices = "", false, nil
		return
	}
	forkChosenRewind(attached, terminal, control, picker, applied, clock)
}

func forkChosenRewind(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, applied preferences.Applied, clock func() time.Time) {
	picker.Kind, picker.Open, picker.Choices = "", false, nil
	picker.cancelScan()
	if err := control.Requests.Fork(&control.Startup, &control.Live, applied, picker.rewindProvider, picker.rewindApprovals, picker.rewind.turn); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Could not rewind session: "+err.Error(), true)
		return
	}
	picker.rewindThread = ""
	attached.presentation.Status.SetText("Forking before selected turn…")
}

type rewindSelection struct {
	snapshot               *changepresentation.Snapshot
	scope                  previewmodel.Scope
	sources                []binding.ThreadInfo
	pending                []string
	turn, command, problem string
}

func (p *composerPicker) readRewindSource(control *sessioncontrol.Client[appServerRequest], parent context.Context) {
	if len(p.rewind.pending) > 0 {
		thread := p.rewind.pending[0]
		if _, err := control.Requests.Send("thread/read", map[string]any{"threadId": thread}, viewRequestScope(p.view), appServerRequest{label: "rewind/source", metadata: thread, menu: p.ID, view: p.view}); err != nil {
			p.rewind.problem = err.Error()
			p.Loading = false
		}
		return
	}
	p.cancelScan()
	if p.rewindSlots == nil {
		p.rewindSlots = make(chan struct{}, 1)
	}
	select {
	case p.rewindSlots <- struct{}{}:
	default:
		p.rewind.problem, p.Loading = "previous history read is still finishing; reopen rewind to retry", false
		return
	}
	if p.scanResults == nil {
		p.scanResults = make(chan pickerScanResult, 1)
	}
	ctx, cancel := context.WithCancel(parent)
	p.scanCancel = cancel
	p.scanID++
	id, results, selection, root, slots := p.scanID, p.scanResults, *p.rewind, p.rewindThread, p.rewindSlots
	go func() {
		defer func() { <-slots }()
		selected, err := sessionreview.RewindChanges(ctx, selection.snapshot, root, selection.turn, selection.sources)
		result := pickerScanResult{id: id, rewind: true}
		if err != nil {
			result.problem = err.Error()
		} else {
			var commands []string
			workspaces := make(map[string]int)
			for _, change := range selected {
				index, exists := workspaces[change.Workspace]
				if !exists {
					index = len(commands)
					workspaces[change.Workspace] = index
					commands = append(commands, "mchanges revert")
				}
				commands[index] += " " + commandargs.Quote(change.ID)
			}
			result.command = strings.Join(commands, " && ")
			result.choices = []completion.Choice{{ID: 2, Name: fmt.Sprintf("Revert %d captured changes", len(selected))}}
		}
		select {
		case results <- result:
		case <-ctx.Done():
		}
	}()
}

func rewindSourceResponse(control *sessioncontrol.Client[appServerRequest], picker *composerPicker, parent context.Context, request appServerRequest, message appserver.Message) {
	if !picker.Open || picker.Kind != "rewind-edits" || picker.ID != request.menu || control.Thread != picker.rewindThread {
		return
	}
	if message.Error != nil {
		picker.rewind.problem, picker.Loading = message.Error.Message, false
		return
	}
	source, err := binding.DecodeHistoryThread(message.Result, request.metadata)
	if err != nil {
		picker.rewind.problem, picker.Loading = err.Error(), false
		return
	}
	picker.rewind.sources = append(picker.rewind.sources, binding.ThreadInfo{ID: source.ID, Path: source.Path})
	picker.rewind.pending = picker.rewind.pending[1:]
	picker.readRewindSource(control, parent)
}

func finishRewindRevert(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], clock func() time.Time) {
	r := control.Rewind
	if r == nil || !r.Completed || control.Turn != "" || control.Shell.BlocksInput(control.Turn) {
		return
	}
	control.Rewind = nil
	control.Replacement.Finish()
	if r.Failed {
		attached.presentation.Status.SetText("Ready")
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Revert incomplete; still in the original session. Inspect the shell output for conflicts and recovery instructions.", true)
		return
	}
	if err := control.Requests.Fork(&control.Startup, &control.Live, r.Settings, r.Provider, r.Approvals, r.BeforeTurn); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Edits reverted, but the session could not be forked: "+err.Error(), true)
		return
	}
	attached.presentation.Status.SetText("Forking before selected turn…")
}

func dispatchRewind(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, clock func() time.Time, parent context.Context, reviewOwner *sessionreview.Review, shellEdits *appServerShellEdits, retained changes.Scope, retainedSession history.Session, changeSource changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publishChanges func([]changes.Update), windows *executionobservation.Windows, audience *sessioncontrol.Audience, captureNotice func(string, string, string, string), reviewSource *changecapture.Service, preparePane func(*scope.Store)) {
	r := control.Rewind
	if r == nil || r.Sent {
		return
	}
	selection := picker.rewind
	if selection == nil || reviewOwner == nil || reviewOwner.Failure != "" || reviewOwner.Restoring() || reviewOwner.Projection.Data != selection.snapshot || !maps.EqualFunc(reviewOwner.Scope().Workspaces, selection.scope.Workspaces, maps.Equal[map[string]bool, map[string]bool]) {
		control.Rewind = nil
		picker.openMenu("rewind-edits")
		picker.Loading = false
		picker.Choices = []completion.Choice{{ID: 1, Name: "Keep edits"}}
		if selection != nil {
			selection.problem = "saved changes changed; reopen /rewind to refresh"
		}
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Saved changes changed; reopen /rewind to refresh before reverting", true)
		return
	}
	command := r.Command
	if err := dispatchShell(attached, terminal, control, clock, parent, reviewOwner, shellEdits, retained, retainedSession, changeSource, snapshots, recovered, publishChanges, windows, audience, captureNotice, reviewSource, preparePane, command); err != nil {
		control.Rewind = nil
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Could not revert edits: "+err.Error(), true)
		return
	}
	r.Sent = true
	control.Replacement.Rewind()
	attached.presentation.Status.SetText("Reverting captured edits…")
}
