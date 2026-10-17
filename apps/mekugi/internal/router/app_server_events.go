package router

import (
	"cmp"

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

func registerThread(session *sessionpresentation.Attachment, info binding.ThreadInfo) {
	s := session
	s.RegisterThread(info.Observation())
	restoreThreadSettings(session, info)
}

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
func (u *appServerUI) sessionEvent(method string, p binding.Notification) (bool, error) {
	s := &u.attached.session
	if s.KnownCaller(u.Thread) == "" {
		return false, nil
	}
	if method == "hook/started" || method == "hook/completed" {
		return true, u.hookEvent(method, p)
	}
	if method == "item/started" {
		observeShellItem(&u.Shell, u.Thread, p.ThreadID, p.Item)
	}
	if command := p.Command; command != nil {
		key := command.Key
		if method == "item/completed" {
			u.execTrack.Completed(key)
		}
		switch {
		case method == "item/started":
			if script, ok := shellinstrumentation.CommandScript(command.Command); ok {
				u.execTrack.Start(key, script)
			}
		case method == "item/completed" && (u.attached.session.Commands()[key] == nil || !u.execTrack.Tracking(key)):
			u.execTrack.Forget(key)
		}
	}

	if h := childHistory(&u.attached.session, &u.childHistoryState, p.ThreadID); h != nil {
		h.state.ObserveLive()
	}
	if p.ThreadID != "" && p.ThreadID != u.Thread {
		if err := requestThreadMetadata(u.Requests, &u.attached.session, p.ThreadID); err != nil {
			return true, err
		}
	}

	key := [3]string{p.ThreadID, p.TurnID, cmp.Or(p.ItemID, p.Item.ID)}
	event := p.Observation(method, presentationTime(u.clock))
	facts := sessionpresentation.ObservationInput{Event: event, Text: binding.RetainedTranscript(event), Main: u.Thread, Selected: u.Turn, Workspace: u.attached.cwd, Tracked: u.execTrack.Tracking(key), Wait: u.readWaitEvidence(event.Item, key)}
	if p.Item.Type == "commandExecution" && u.nativeTrace != nil {
		if ms, known := u.nativeTrace.CommandTimeout(p.ThreadID, key[2]); known {
			facts.CommandTimeout = strconv.FormatUint(ms/1000, 10)
			if fraction := ms % 1000; fraction != 0 {
				facts.CommandTimeout += "." + strings.TrimRight(fmt.Sprintf("%03d", fraction), "0")
			}
			facts.CommandTimeout += "s"
		}
	}
	if method == "turn/completed" {
		endTurnPreviews(u.execWindows, u.review, u.attached.cwd, u.shell, presentationTime(u.clock), p.ThreadID, p.Turn.ID)
	}
	result := sessionpresentation.ReduceObservation(s, u.attached.presentation, u.attached.source, u.attached.childSource, u.attached.transcript, &u.attached.waits, u.activity.Outputs(s.Scope()), facts)
	if result.Started && u.review != nil {
		u.review.Preview().SetTurn(p.ThreadID, p.Turn.ID, true)
	}
	if method == "thread/started" && p.Thread.ID != "" && p.Thread.ID != u.Thread {
		restoreThreadSettings(&u.attached.session, p.Thread)
	}
	if method == "thread/tokenUsage/updated" || method == "turn/completed" {
		if method == "thread/tokenUsage/updated" && p.ThreadID == u.Thread {
			u.exitUsage = p.TokenUsage.Total
		}
		observeCost(u.usage, p.ThreadID, s.Agent(s.Caller(p.ThreadID)))
	}
	if p.Item.Type == "subAgentActivity" && p.Item.AgentThreadID != "" && p.Item.AgentThreadID != u.Thread {
		if err := requestThreadMetadata(u.Requests, &u.attached.session, p.Item.AgentThreadID); err != nil {
			return true, err
		}
	}
	u.retainWaitProgress(key, result.Wait)
	if u.usage != nil {
		for _, thread := range result.Spawned {
			u.usage.MarkNew(thread)
		}
	}
	if result.Publish {
		u.streams.Deliver(result.Deliveries, u.shell.Transcript, u.shell.Agents)
		u.applyActivity(nil, nil)
	}
	return result.Handled, nil
}

// flushStreamOutput advances terminal frames and admits changed segment facts.
// Reveal-only updates never replace neutral command records. Late output cannot
// reopen a completed invocation.
func (u *appServerUI) flushStreamOutput() {
	u.dirty = u.streams.AdvanceReasoning(presentationTime(u.clock), u.shell.Transcript, u.shell.Agents) || u.dirty
	s := &u.attached.session
	var entries []sessionpresentation.ActivityEntry
	handled := make(map[[3]string]bool)
	for key, run := range u.attached.session.Commands() {
		tracked, trackedCommand := u.flushTrackedCommand(key, run)
		entries = append(entries, tracked...)
		handled[key] = trackedCommand
	}
	u.dirty = u.streams.AdvanceCommands(presentationTime(u.clock), handled, u.shell.Transcript, u.shell.Agents) || u.dirty

	if len(entries) == 0 {
		return
	}
	slices.SortFunc(entries, func(a, b sessionpresentation.ActivityEntry) int { return cmp.Compare(a.Seq, b.Seq) })
	for i := range entries {
		entries[i].Seq = s.Activity().NextSequence()
	}
	u.applyActivity(entries, nil)
	u.dirty = true
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
