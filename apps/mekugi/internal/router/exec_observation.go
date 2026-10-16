package router

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
)

const maxExecReportBytes = 8 << 10

// requestSessionShell reads the latest session environment shell. Multiple environments with different shells leave it
// unknown.
func requestSessionShell(raw json.RawMessage) string {
	var input []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return ""
	}
	shell := ""
	for _, item := range input {
		if item.Type != "message" && item.Type != "" {
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item.Content, &parts) != nil {
			continue
		}
		for _, part := range parts {
			text := strings.TrimSpace(part.Text)
			if !strings.HasPrefix(text, "<environment_context>") || !strings.HasSuffix(text, "</environment_context>") {
				continue
			}
			var shells []string
			for rest := text; ; {
				_, after, found := strings.Cut(rest, "<shell>")
				if !found {
					break
				}
				value, remainder, closed := strings.Cut(after, "</shell>")
				if !closed {
					break
				}
				shells, rest = append(shells, html.UnescapeString(value)), remainder
			}
			if len(shells) == 0 {
				// An update that omits the shell keeps the previous one.
				continue
			}
			shell = shells[0]
			for _, other := range shells[1:] {
				if other != shell {
					shell = ""
				}
			}
		}
	}
	return shell
}

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
func (p *mekugiProxy) finalizeExecObservations(ctx context.Context, workspace string, members []execCompletion) error {
	var pending []execCompletion
	for _, member := range members {
		if member.history.ExecObservation == nil || !member.result.Terminal {
			continue
		}
		derived := legacycall.ExecCallID(member.callID, member.history.ExecObservation.CodeMode)
		retained, found, err := p.replayStore.lookup(ctx, workspace, derived)
		if err != nil {
			return err
		}
		if found {
			if retained.CorrelationID != member.callID+"\x00exec" {
				return errors.New("retained stock exec_command observation is inconsistent")
			}
			continue
		}
		pending = append(pending, member)
	}
	members = pending
	if len(members) == 0 {
		return nil
	}
	first := members[0]
	thread := first.history.ExecutingThread
	derivedCallID := legacycall.ExecCallID(first.callID, first.history.ExecObservation.CodeMode)
	correlation := first.callID + "\x00exec"
	refs := make([]string, 0, len(members))
	observations := make([]changecapture.Observation, 0, len(members))
	var tree string
	var earliest uint64
	for _, member := range members {
		refs = append(refs, member.callID)
		observations = append(observations, *member.history.ExecObservation)
		// Request sequence selects the checkpoint that bounds every sibling;
		// command and report order remains the original member order.
		if checkpoint := member.history.ExecObservation.Tree; checkpoint != "" && (tree == "" || member.history.sequence < earliest) {
			tree, earliest = checkpoint, member.history.sequence
		}
	}
	observation := changecapture.Merge(observations)
	observation.Tree = tree
	script := execObservationScript(observation)
	if observation.Tree != "" && p.replayStore != nil {
		// Closing, comparing, and claiming are one step per workspace, so
		// concurrent finalizations cannot both record one effect.
		defer p.replayStore.snapshots.Serialize(workspace)()
	}
	view := p.execWindows.close(refs...)
	for _, path := range view.created {
		observation.AddCreatedPath(path.Data, path.Tree)
	}
	background := slices.Compact(slices.Sorted(slices.Values(view.background)))
	comparison := changecapture.Comparison{Excluded: view.excluded, Workspace: workspace, Claims: view.claims}
	if view.snapshot && p.replayStore != nil {
		comparison.Workspaces = p.replayStore.snapshots
	}
	result := changecapture.Reconcile(ctx, observation, comparison)
	p.execWindows.claim(refs, result.Claims)
	reviews, complete := result.Files, result.Complete
	outcome := &result.Outcome
	outcome.Overlaps, outcome.Background = view.overlaps, background
	var reports, failureReports []string
	var hostResults []binding.ToolResult
	for index, member := range members {
		exit, completed, resultText := member.result.ExitCode, member.result.Succeeded, member.result.Text
		status := changecapture.StatusCompleted
		switch {
		case member.history.ExecObservation.CodeMode && completed:
			// Outer script completion does not prove each nested command
			// succeeded; nested exit codes are not visible to the router.
			status = ""
		case !completed:
			status = changecapture.StatusFailed
		}
		if results, confirmed := member.history.nativeCell.Commands(member.history.ExecObservation.Commands, workspace); confirmed {
			status = changecapture.StatusCompleted
			hostResults = append(hostResults, results...)
			for _, result := range results {
				if result.Status != "completed" || result.ExitCode == nil || *result.ExitCode != 0 {
					status = changecapture.StatusFailed
				}
			}
			if len(results) == 1 {
				exit = results[0].ExitCode
			}
		}
		if member.host != nil {
			hostResults = append(hostResults, *member.host)
		}
		if len(members) == 1 {
			outcome.Exit = exit
			reports = append(reports, execTruncateReport(resultText, maxExecReportBytes))
		} else {
			reports = append(reports, fmt.Sprintf("# call %d\n%s", index+1, execTruncateReport(resultText, maxExecReportBytes/len(members))))
		}
		if index == 0 || status == changecapture.StatusFailed || status == "" && outcome.Status != changecapture.StatusFailed {
			outcome.Status = status
		}
		if status == changecapture.StatusFailed {
			failureReports = append(failureReports, execTruncateReport(resultText, maxExecReportBytes))
		}
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
		Attempt:         1,
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
	if len(reviews) != 0 && p.replayStore != nil {
		changeID, err := p.replayStore.changes.Reserve(ctx, p.replayStore.scoped(ctx).changeScope(), workspace, thread, correlation)
		if err != nil {
			return changeError(err)
		}
		record.ChangeID = changeID
	}
	if !complete && record.Report != "" {
		record.Report += "\nMekugi could not capture complete file evidence."
	}
	if p.replayStore != nil && outcome.Status == changecapture.StatusFailed && len(failureReports) != 0 {
		// Retain the bounded host evidence, not an invented reconstruction of
		// output the host omitted. This never changes the host's tool result.
		output := command.OmittedOutput{Stdout: strings.Join(failureReports, "\n")}
		// The read record's status only marks the output failed; the observed
		// exit, including an unknown or out-of-range one, stays on the outcome.
		exit := 1
		if outcome.Exit != nil && *outcome.Exit > 0 && *outcome.Exit <= 255 {
			exit = *outcome.Exit
		}
		session, err := p.replayStore.recoverySession(ctx)
		var ref string
		if err == nil {
			ref, err = session.Publish(ctx, output, exit)
		}
		if err != nil {
			p.notice("", thread, "failure_output_storage", "Failed-command output could not be retained. The command outcome remains available without its output.")
		} else {
			outcome.OutputRef = ref
		}
	}
	records := map[string]mekugiHistory{derivedCallID: record}
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
	if err := p.replayStore.put(context.WithoutCancel(ctx), workspace, records); err != nil {
		return err
	}
	if record.ChangeID != "" {
		_ = p.replayStore.publishEditReceipt(context.WithoutCancel(ctx), workspace, thread, derivedCallID, p.activity, responses.StringField(first.history.UpstreamItem, "id"))
	}
	return nil
}
