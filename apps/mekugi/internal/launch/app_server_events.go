package launch

import (
	"cmp"
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	commandsegments "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/modules/usage/accounting"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
)

func restoreThreadSettings(session *sessionpresentation.Attachment, info binding.ThreadInfo) {
	s := session
	path := s.KnownCaller(info.ID)
	restoreContextUsage(s.Agent(path), info)
	if agent := s.Agent(path); !agent.SettingsLive {
		settings := rollout.ResumeSettings(info.ID, info.Path, time.Time{})
		if model, ok := settings["model"].(string); ok {
			agent.Settings.Model = model
			agent.Settings.Effort, _ = settings["model_reasoning_effort"].(string)
			if tier, known := settings["service_tier"]; known {
				agent.Settings.Tier, _ = tier.(string)
				agent.Settings.TierKnown = true
			}
		}
	}
}

// sessionEvent owns live enrichment and effects around shared presentation.
func sessionEvent(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], streams *terminalsession.Streams, tracker *commandsegments.Observer, activity *activitymodel.Service, usage *accounting.Service, trace *nativetrace.Reader, windows *executionobservation.Windows, reviewOwner *sessionreview.Review, approvals *nativeApprovalDock, receipts *[]*changepresentation.Receipt, retainedSession history.Session, exitUsage *binding.TokenUsage, clock func() time.Time, method string, p binding.Notification) (bool, error) {
	s := &attached.session
	if s.KnownCaller(control.Thread) == "" {
		return false, nil
	}
	if p.ThreadID == control.Thread && p.TurnID != "" && p.Item.Type == "userMessage" && (method == "item/started" || method == "item/completed") {
		control.HasUserTurn = true
	}
	if p.ThreadID != "" && p.ThreadID != control.Thread {
		s.Caller(p.ThreadID)
		agent := s.Activity().Agent(p.ThreadID)
		if control.Requests.WorkSide(p.ThreadID) {
			agent.Isolated, agent.AudiencePending = true, false
		} else if side := attached.conversation.Side; side != nil && side.OneOff && agent.ParentThread == "" {
			agent.AudiencePending = true
		}
	}
	if method == "hook/started" || method == "hook/completed" {
		return true, hookEvent(attached, terminal, control, activity, reviewOwner, approvals, receipts, retainedSession, clock, method, p)
	}
	if method == "item/started" {
		observeShellItem(&control.Shell, control.Thread, p.ThreadID, p.Item)
	}
	if command := p.Command; command != nil {
		key := command.Key
		if method == "item/completed" {
			tracker.Completed(key)
		}
		switch {
		case method == "item/started":
			if script, ok := shellinstrumentation.CommandScript(command.Command); ok {
				tracker.Start(key, script)
			}
		case method == "item/completed" && (attached.session.Commands()[key] == nil || !tracker.Tracking(key)):
			tracker.Forget(key)
		}
	}

	key := [3]string{p.ThreadID, p.TurnID, cmp.Or(p.ItemID, p.Item.ID)}
	event := p.Observation(method, presentationTime(clock))

	facts := sessionpresentation.ObservationInput{Event: event, Text: binding.RetainedTranscript(event), Main: control.Thread, Selected: control.Turn, Start: control.InterruptTarget().Start, Workspace: attached.cwd, Tracked: tracker.Tracking(key)}
	facts.AuxiliaryTurn = p.ThreadID == control.Thread && control.AuxiliaryTurn(p.Turn.ID)
	if p.Item.Type == "commandExecution" && trace != nil {
		if ms, known := trace.CommandTimeout(p.ThreadID, key[2]); known {
			facts.CommandTimeout = strconv.FormatUint(ms/1000, 10)
			if fraction := ms % 1000; fraction != 0 {
				facts.CommandTimeout += "." + strings.TrimRight(fmt.Sprintf("%03d", fraction), "0")
			}
			facts.CommandTimeout += "s"
		}
	}
	if method == "turn/completed" {
		endTurnPreviews(windows, reviewOwner, attached.cwd, terminal, presentationTime(clock), p.ThreadID, p.Turn.ID)
	}
	result := sessionpresentation.ReduceObservation(s, attached.presentation, attached.source, attached.childSource, attached.transcript, activity.Outputs(s.Scope()), facts)
	if result.Started && reviewOwner != nil {
		reviewOwner.Preview().SetTurn(p.ThreadID, p.Turn.ID, true)
	}
	if method == "thread/started" && p.Thread.ID != "" && p.Thread.ID != control.Thread {
		restoreThreadSettings(&attached.session, p.Thread)
	}
	if method == "thread/tokenUsage/updated" || method == "turn/completed" {
		if method == "thread/tokenUsage/updated" && p.ThreadID == control.Thread {
			(*exitUsage) = p.TokenUsage.Total
		}
		observeCost(usage, p.ThreadID, s.Agent(s.Caller(p.ThreadID)))
	}
	if result.Publish {
		streams.Deliver(result.Deliveries, terminal.Transcript, terminal.Agents)
		if attached.deliverActivity != nil {
			attached.deliverActivity(attached.executionPresentation, result.Deliveries)
		}
		applyActivity(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, nil, nil)
	}
	return result.Handled, nil
}

// flushStreamOutput advances terminal frames and admits changed segment facts.
// Reveal-only updates never replace neutral command records. Late output cannot
// reopen a completed invocation.
func flushStreamOutput(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], streams *terminalsession.Streams, tracker *commandsegments.Observer, reviewOwner *sessionreview.Review, approvals *nativeApprovalDock, receipts *[]*changepresentation.Receipt, clock func() time.Time, parent context.Context, retained changes.Scope, segments *reports.Service, writes *chan commandSegmentWrite, pending *int, issues *requestexecution.Failures) {
	streams.AdvanceReasoning(presentationTime(clock), terminal.Transcript, terminal.Agents)
	s := &attached.session
	var entries []sessionpresentation.ActivityEntry
	handled := make(map[[3]string]bool)
	for key, run := range attached.session.Commands() {
		tracked, trackedCommand := flushTrackedCommand(attached, terminal, streams, tracker, clock, parent, retained, segments, writes, pending, issues, key, run)
		entries = append(entries, tracked...)
		handled[key] = trackedCommand
	}
	streams.AdvanceCommands(presentationTime(clock), handled, terminal.Transcript, terminal.Agents)

	if len(entries) == 0 {
		return
	}
	slices.SortFunc(entries, func(a, b sessionpresentation.ActivityEntry) int { return cmp.Compare(a.Seq, b.Seq) })
	for i := range entries {
		entries[i].Seq = s.Activity().NextSequence()
	}
	applyActivity(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, entries, nil)
}

// observeCost reads all consumption metrics from the same canonical owner.
// App-server context and exit usage are separate, never priced or added here.
func observeCost(usage *accounting.Service, thread string, agent *sessionpresentation.ActivityAgent) {
	if usage == nil {
		return
	}
	report, observed := usage.Snapshot(thread)
	agent.Roundtrips = report.Roundtrips
	agent.OutputThroughput = report.RoundOutput.Throughput
	agent.InputTokens, agent.OutputTokens = report.InputTokens, report.OutputTokens
	agent.TokensKnown = observed && agent.Roundtrips > report.MissingUsage
	agent.Cost = report.Cost.CachedInput + report.Cost.UncachedInput + report.Cost.Output
	agent.CostKnown = agent.TokensKnown && report.Cost.Known
	agent.CostPartial = report.MissingUsage != 0 || report.PriorUnknown
	agent.UsagePartial = agent.CostPartial
	agent.RoundtripsPartial = report.PriorUnknown
}

func restoreUsage(usage *accounting.Service, info binding.ThreadInfo) {
	if usage != nil {
		usage.Restore(info.ID, len(info.Turns) > 0)
	}
}
