package launch

import (
	"cmp"
	"context"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandargs"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/completion"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func showRewindPicker(control *sessioncontrol.Client[appServerRequest], picker *composerPicker, review *sessionreview.Review) {
	picker.cancelScan()
	picker.openMenu("rewind")
	picker.Target, picker.Choices, picker.Problem, picker.Loading = completion.Target{}, nil, "", true
	picker.rewindThread = control.Thread
	picker.rewind = &rewindSelection{problem: "saved change evidence is unavailable"}
	if review != nil && review.Projection.Retained.Store != nil && review.Projection.Data != nil && review.Failure == "" && !review.Restoring() {
		picker.rewind.snapshot, picker.rewind.problem = review.Projection.Data, ""
		picker.rewind.scope = review.Projection.Scope
	}
	if _, err := control.Requests.Send("thread/read", map[string]any{"threadId": control.Thread}, viewRequestScope(picker.view), appServerRequest{label: "rewind/turns", metadata: control.Thread, menu: picker.ID, view: picker.view}); err != nil {
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
	if thread.HistoryMode != "paginated" {
		picker.Problem = "Rewind requires paginated history; legacy sessions cannot be rewound"
		return
	}
	picker.rewind.sources = []binding.ThreadInfo{{ID: thread.ID, Path: thread.Path}}
	picker.Loading = true
	if _, err := control.Requests.Send("thread/turns/list", historyPageParams(thread.ID, ""), viewRequestScope(picker.view), appServerRequest{label: "rewind/page", metadata: thread.ID, menu: picker.ID, view: picker.view}); err != nil {
		picker.Loading, picker.Problem = false, "Could not load user turns: "+err.Error()
	}
}

func rewindPageResponse(control *sessioncontrol.Client[appServerRequest], picker *composerPicker, request appServerRequest, message appserver.Message) {
	if !picker.Open || picker.Kind != "rewind" || picker.ID != request.menu || picker.rewindThread != request.metadata || control.Thread != request.metadata {
		return
	}
	picker.Loading = false
	var page appServerHistoryPage
	if message.Error != nil {
		picker.Choices = nil
		picker.Problem = "Could not load user turns: " + message.Error.Message
		return
	}
	if err := json.Unmarshal(message.Result, &page); err != nil {
		picker.Choices = nil
		picker.Problem = "Could not load user turns: " + err.Error()
		return
	}
	for _, turn := range page.Data {
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
			picker.Choices = append(picker.Choices, completion.Choice{Name: turn.ID, Display: cmp.Or(text, "User turn (no text)"), Description: "Rewind immediately before this turn"})
			break
		}
	}
	if page.NextCursor != "" {
		if picker.rewind.cursors == nil {
			picker.rewind.cursors = make(map[string]bool)
		}
		if picker.rewind.cursors[page.NextCursor] {
			picker.Choices, picker.Problem = nil, "Could not load user turns: history cursor did not advance"
			return
		}
		picker.rewind.cursors[page.NextCursor] = true
		picker.Loading = true
		if _, err := control.Requests.Send("thread/turns/list", historyPageParams(control.Thread, page.NextCursor), viewRequestScope(picker.view), request); err != nil {
			picker.Loading, picker.Problem, picker.Choices = false, "Could not load user turns: "+err.Error(), nil
		}
		return
	}
	if len(picker.Choices) == 0 {
		picker.Problem = "No user turns available to rewind"
	}
}

func chooseRewind(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, restoring bool, clock func() time.Time, choice completion.Choice, parent context.Context) {
	if restoring || control.Replacement.Pending() || control.Thread != picker.rewindThread || sessionBusy(&control.Live, attached.conversation) {
		setNotice(&attached.feedback, presentationTime(clock), "Wait for the session to be idle before rewinding", true)
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
				revertChosenRewind(attached, control, picker, clock)
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
		control.Rewind = &sessioncontrol.RewindOperation{Thread: control.Thread, BeforeTurn: picker.rewind.turn, Command: picker.rewind.command}
		picker.Kind, picker.Open, picker.Choices = "", false, nil
		return
	}
	revertChosenRewind(attached, control, picker, clock)
}

func revertChosenRewind(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, clock func() time.Time) {
	picker.Kind, picker.Open, picker.Choices = "", false, nil
	picker.cancelScan()
	if err := requestRewind(control, picker.rewind.turn); err != nil {
		setNotice(&attached.feedback, presentationTime(clock), "Could not rewind session: "+err.Error(), true)
		return
	}
	picker.rewindThread = ""
	attached.presentation.Status.SetText("Rewinding before selected turn…")
}

func requestRewind(control *sessioncontrol.Client[appServerRequest], before string) error {
	if _, err := control.Requests.Send("thread/revert", map[string]any{"threadId": control.Thread, "beforeTurnId": before}, "main", appServerRequest{}); err != nil {
		return err
	}
	control.Replacement.Rewind()
	return nil
}

func continueRewind(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, restoring bool, clock func() time.Time) {
	if picker.Open && picker.Kind == "rewind-edits" && !picker.Loading && picker.rewind.problem == "" && picker.rewind.command == "" && !restoring && !control.Replacement.Pending() && control.Thread == picker.rewindThread && !sessionBusy(&control.Live, attached.conversation) {
		revertChosenRewind(attached, control, picker, clock)
	}
}

type rewindSelection struct {
	cursors                map[string]bool
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

func finishRewindRevert(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], clock func() time.Time) {
	r := control.Rewind
	if r == nil || !r.Completed || control.Turn != "" || control.Shell.BlocksInput(control.Turn) {
		return
	}
	control.Rewind = nil
	control.Replacement.Finish()
	if r.Failed {
		attached.presentation.Status.SetText("Ready")
		setNotice(&attached.feedback, presentationTime(clock), "Revert incomplete; still in the original session. Inspect the shell output for conflicts and recovery instructions.", true)
		return
	}
	if err := requestRewind(control, r.BeforeTurn); err != nil {
		setNotice(&attached.feedback, presentationTime(clock), "Edits reverted, but the conversation could not be rewound: "+err.Error(), true)
		return
	}
	attached.presentation.Status.SetText("Rewinding before selected turn…")
}

func (session *nativeSession) dispatchRewind(attached *attachedPresentation, picker *composerPicker, clock func() time.Time, parent context.Context) {
	r := session.Client.Rewind
	if r == nil || r.Sent {
		return
	}
	selection := picker.rewind
	if selection == nil || session.review == nil || session.review.Failure != "" || session.review.Restoring() || session.review.Projection.Data != selection.snapshot || !maps.EqualFunc(session.review.Scope().Workspaces, selection.scope.Workspaces, maps.Equal[map[string]bool, map[string]bool]) {
		session.Client.Rewind = nil
		picker.openMenu("rewind-edits")
		picker.Loading = false
		picker.Choices = []completion.Choice{{ID: 1, Name: "Keep edits"}}
		if selection != nil {
			selection.problem = "saved changes changed; reopen /rewind to refresh"
		}
		setNotice(&attached.feedback, presentationTime(clock), "Saved changes changed; reopen /rewind to refresh before reverting", true)
		return
	}
	command := r.Command
	if err := session.dispatchShell(attached, clock, parent, command); err != nil {
		session.Client.Rewind = nil
		setNotice(&attached.feedback, presentationTime(clock), "Could not revert edits: "+err.Error(), true)
		return
	}
	r.Sent = true
	session.Client.Replacement.Rewind()
	attached.presentation.Status.SetText("Reverting captured edits…")
}
