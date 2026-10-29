package executionobservation

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

const maxExecReportBytes = 8 << 10

func execObservationScript(observation changecapture.Observation) string {
	if len(observation.Commands) == 0 {
		return "# exec commands are not literal\n"
	}
	if !observation.CodeMode && len(observation.Commands) == 1 {
		return observation.Commands[0].Command
	}
	var script strings.Builder
	for index, command := range observation.Commands {
		if index != 0 {
			script.WriteByte('\n')
		}
		fmt.Fprintf(&script, "# tools.exec_command %d\n%s\n", index+1, strings.TrimSuffix(command.Command, "\n"))
	}
	return script.String()
}

// execCompletion pairs a decoded host result with its original observation.
type execCompletion struct {
	callID  string
	history mekugiHistory
	result  binding.ExecutionResult
	host    *binding.ToolResult // Native Shell Mode has an item result, not a tool-output header.
	native  bool                // Preserve a native tool's identity and input without a stock replay carrier.
	unknown bool                // The process joined, but no terminal native tool outcome was observed.
}

// execSiblingKey groups calls of one response. Siblings finalized in
// the same request share one record, since their windows are the same.
func execSiblingKey(history mekugiHistory) string {
	if observation := history.ExecObservation; observation != nil && observation.Group != "" {
		return observation.Group
	}
	return ""
}

func execTruncateReport(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n[host result truncated]"
}

func execDerivedUpstreamItem(derivedCallID, arguments string) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"type":      responses.MustMarshalJSON("function_call"),
		"name":      responses.MustMarshalJSON(nativeExecCommandToolName),
		"call_id":   responses.MustMarshalJSON(derivedCallID),
		"arguments": responses.MustMarshalJSON(arguments),
		"status":    responses.MustMarshalJSON("completed"),
	}
}

// finalizeExecObservations persists the observed effect of terminal command
// results. Members are parallel siblings that share one record, keyed by the
// first; the others keep a record naming it. An empty effect is retained
// without a change ID, so replay neither reads the workspace again nor
// allocates an ID for read-only work.
func finalizeExecObservations(selected changes.Scope, source changes.Service, snapshots *changecapture.Workspaces, recovered *recovery.Service, publish func([]changes.Update), windows *Windows, activity *sessioncontrol.Audience, notice func(string, string, string, string), ctx context.Context, workspace string, members []execCompletion) error {
	selected = observationScope(ctx, selected)
	var pending []execCompletion
	for _, member := range members {
		if member.history.ExecObservation == nil || !member.result.Terminal {
			continue
		}
		derived := legacycall.ExecCallID(member.callID, member.history.ExecObservation.CodeMode)
		retained, found, err := selected.Store.LookupHostCall(ctx, workspace, derived)
		if err != nil {
			return err
		}
		if found {
			if retained.CorrelationID != member.callID+"\x00exec" {
				return errors.New("retained stock exec_command observation is inconsistent")
			}
			if member.native && retained.ChangeID != "" {
				completed := legacyHistory(retained)
				completed.confirmed = true
				updates, err := source.Repair(ctx, selected, workspace, changePublications(map[string]mekugiHistory{derived: completed}))
				if err != nil {
					return changeError(err)
				}
				notifyChanges(publish, updates)
			}
			if retained.ChangeID == "" && retained.Attempt > 1 {
				completed := legacyHistory(retained)
				completed.confirmed = true
				if _, err := source.Repair(ctx, selected, workspace, changePublications(map[string]mekugiHistory{derived: completed})); err != nil {
					return changeError(err)
				}
			}
			continue
		}
		pending = append(pending, member)
	}
	members = pending
	if len(members) == 0 {
		return nil
	}
	var intervalRefs []string
	for _, member := range members {
		intervalRefs = append(intervalRefs, member.callID)
	}
	cuts := windows.intervalCuts(intervalRefs)
	for _, cut := range cuts {
		intervalRefs = append(intervalRefs, cut.writer)
	}
	// Every affected writer is locked before workspace comparison/publication.
	// Resumption must not acquire another writer beneath the workspace lock.
	unlockIntervals := windows.lockIntervals(intervalRefs)
	defer unlockIntervals()
	var intervals []execInterval
	for i := range members {
		member := &members[i]
		parts, err := readExecIntervals(ctx, selected, source, workspace, member.callID, member.history.ExecObservation.CodeMode)
		if err != nil {
			return err
		}
		if len(parts) != 0 && len(members) > 1 {
			unlockIntervals()
			// An independently witnessed edit boundary separates this writer
			// from siblings that otherwise share one coarse response window.
			for _, single := range members {
				if err := finalizeExecObservations(selected, source, snapshots, recovered, publish, windows, activity, notice, ctx, workspace, []execCompletion{single}); err != nil {
					return err
				}
			}
			return nil
		}
		intervals = append(intervals, parts...)
		member.history.ExecObservation = rebaseExecIntervals(member.history.ExecObservation, parts)
	}
	first := members[0]
	thread := first.history.ExecutingThread
	derivedCallID := legacycall.ExecCallID(first.callID, first.history.ExecObservation.CodeMode)
	correlation := first.callID + "\x00exec"
	refs := make([]string, 0, len(members))
	observations := make([]changecapture.Member, 0, len(members))
	for _, member := range members {
		refs = append(refs, member.callID)
		observations = append(observations, changecapture.Member{Observation: *member.history.ExecObservation, Sequence: member.history.sequence})
	}
	observation := changecapture.Merge(observations)
	script := first.history.Script
	if !first.native {
		script = execObservationScript(observation)
	}
	if observation.Tree != "" && selected.Store != nil {
		// Closing, comparing, and claiming are one step per workspace, so
		// concurrent finalizations cannot both record one effect.
		defer windows.serialize(workspace)()
	}
	view := windows.close(refs...)
	for _, path := range view.created {
		observation.AddCreatedPath(path.Data, path.Tree)
	}
	background := slices.Compact(slices.Sorted(slices.Values(view.background)))
	comparison := changecapture.Comparison{Excluded: view.excluded, Workspace: workspace, Claims: view.claims}
	if view.snapshot && selected.Store != nil {
		comparison.Workspaces = snapshots
	}
	result := changecapture.Reconcile(ctx, observation, comparison)
	windows.claim(refs, result.Claims)
	reviews, complete := result.Files, result.Complete
	outcome := &result.Outcome
	outcome.Overlaps, outcome.Background = view.overlaps, background
	var reports, failureReports []string
	var hostResults []binding.ToolResult
	completions := make([]changecapture.Completion, 0, len(members))
	for index, member := range members {
		completion := changecapture.Completion{CodeMode: member.history.ExecObservation.CodeMode, Succeeded: member.result.Succeeded, Exit: member.result.ExitCode}
		if results, confirmed := member.history.nativeCell.Commands(member.history.ExecObservation.Commands, workspace); confirmed {
			completion.Confirmed = true
			hostResults = append(hostResults, results...)
			for _, result := range results {
				completion.Commands = append(completion.Commands, changecapture.CommandOutcome{Completed: result.Status == "completed", Exit: result.ExitCode})
			}
		}
		completions = append(completions, completion)
		if member.host != nil {
			hostResults = append(hostResults, *member.host)
		}
		if len(members) == 1 {
			reports = append(reports, execTruncateReport(member.result.Text, maxExecReportBytes))
		} else {
			reports = append(reports, fmt.Sprintf("# call %d\n%s", index+1, execTruncateReport(member.result.Text, maxExecReportBytes/len(members))))
		}
	}
	for index, failed := range outcome.ApplyCompletions(completions) {
		if failed && !members[index].unknown {
			failureReports = append(failureReports, execTruncateReport(members[index].result.Text, maxExecReportBytes))
		}
	}
	if len(members) == 1 && first.unknown {
		outcome.Status, outcome.Exit = "", nil
		outcome.ScopeReason = strings.TrimPrefix(outcome.ScopeReason+"; native tool outcome unavailable after joined runtime cleanup", "; ")
	}
	arguments := first.history.CarrierPayload
	if len(members) != 1 || observation.CodeMode || arguments == "" {
		arguments = string(responses.MustMarshalJSON(map[string]string{"cmd": script}))
	}
	record := mekugiHistory{
		ToolName:        nativeExecCommandToolName,
		Script:          script,
		Root:            workspace,
		ExecutingThread: thread,
		Caller:          first.history.Caller,
		Source:          observation.SourceLabel(),
		CorrelationID:   correlation,
		Attempt:         len(intervals) + 1,
		ReviewFiles:     reviews,
		Report:          strings.Join(reports, "\n"),
		ExecOutcome:     outcome,
		HostResults:     hostResults,
		CarrierKind:     codeModeCarrierFunction,
		CarrierName:     nativeExecCommandToolName,
		CarrierPayload:  arguments,
		ReplayCarrier:   true,
		UpstreamItem:    execDerivedUpstreamItem(derivedCallID, arguments),
	}
	if first.native {
		record.ToolName, record.CarrierName = first.history.ToolName, first.history.CarrierName
		record.CarrierPayload, record.UpstreamItem = first.history.CarrierPayload, first.history.UpstreamItem
		record.ReplayCarrier = false
		if record.Source == nativeExecCommandToolName {
			record.Source = first.history.ToolName
		}
	}
	if len(intervals) != 0 && (len(reviews) != 0 || slices.ContainsFunc(intervals, func(interval execInterval) bool { return len(interval.before.ReviewFiles) != 0 })) {
		record.ChangeID = intervals[0].before.ChangeID
	}
	if len(reviews) != 0 && selected.Store != nil {
		changeID, err := source.Reserve(ctx, selected, workspace, thread, correlation)
		if err != nil {
			return changeError(err)
		}
		record.ChangeID = changeID
	}
	if !complete && record.Report != "" {
		record.Report += "\nMekugi could not capture complete file evidence."
	}
	if selected.Store != nil && outcome.Status == changecapture.StatusFailed && len(failureReports) != 0 {
		// Retain the bounded host evidence, not an invented reconstruction of
		// output the host omitted. This never changes the host's tool result.
		output := command.OmittedOutput{Stdout: strings.Join(failureReports, "\n")}
		// The read record's status only marks the output failed; the observed
		// exit, including an unknown or out-of-range one, stays on the outcome.
		exit := 1
		if outcome.Exit != nil && *outcome.Exit > 0 && *outcome.Exit <= 255 {
			exit = *outcome.Exit
		}
		session, err := recovered.AttachStore(selected.Store, selected.Namespace, selected.Thread, selected.Limits, selected.Writes)
		var ref string
		if err == nil {
			ref, err = session.Publish(ctx, output, exit)
		}
		if err != nil {
			if notice != nil {
				notice("", thread, "failure_output_storage", "Failed-command output could not be retained. The command outcome remains available without its output.")
			}
		} else {
			outcome.OutputRef = ref
		}
	}
	records := map[string]mekugiHistory{derivedCallID: record}
	if record.ChangeID != "" {
		for _, interval := range intervals {
			records[interval.call] = interval.before
		}
	}
	for _, member := range members[1:] {
		memberObservation := *member.history.ExecObservation
		memberCallID := legacycall.ExecCallID(member.callID, memberObservation.CodeMode)
		memberScript := execObservationScript(memberObservation)
		memberArguments := string(responses.MustMarshalJSON(map[string]string{"cmd": memberScript}))
		records[memberCallID] = mekugiHistory{
			ToolName: nativeExecCommandToolName, Script: memberScript, Root: workspace,
			ExecutingThread: cmp.Or(member.history.ExecutingThread, thread), Caller: member.history.Caller,
			CorrelationID: member.callID + "\x00exec", Attempt: 1,
			ExecOutcome: &changecapture.Outcome{
				Status: outcome.Status, Class: memberObservation.Class, Labels: memberObservation.Labels,
				Coverage: outcome.Coverage, CodeMode: memberObservation.CodeMode, SharedWith: derivedCallID,
			},
			CarrierKind: codeModeCarrierFunction, CarrierName: nativeExecCommandToolName, CarrierPayload: memberArguments,
			ReplayCarrier: true, UpstreamItem: execDerivedUpstreamItem(memberCallID, memberArguments),
		}
	}
	continuations, err := prepareExecIntervalContinuations(ctx, workspace, cuts, result.Endpoints)
	if err != nil {
		return err
	}
	if err := publishObservations(context.WithoutCancel(ctx), selected, source, publish, workspace, records, continuations...); err != nil {
		return err
	}
	if record.ChangeID != "" {
		_ = publishEditReceipt(context.WithoutCancel(ctx), selected, source, workspace, thread, derivedCallID, activity, responses.StringField(first.history.UpstreamItem, "id"))
	}
	return nil
}
