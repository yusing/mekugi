package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi"
	codexinstructions "github.com/yusing/mekugi/contrib/codex"
	"github.com/yusing/mekugi/internal/hpatchsyntax"
)

const mekugiRecoveryToolName = "hpatch_recover"

func mekugiRecoveryGuidance(
	script string,
	rejections []mekugi.HostRejection,
	refreshed bool,
	handles []string,
) string {
	notice := invalidFinalInputNotice(script, rejections)
	references, eligible := mekugiRecoveryReferences(script, rejections, refreshed, handles)
	if !eligible {
		return notice + genericRecoveryGuidance(script, rejections, refreshed, handles)
	}
	return notice + codexinstructions.RecoveryGuidance(references)
}

func invalidFinalInputNotice(script string, rejections []mekugi.HostRejection) string {
	if len(rejections) != 1 || rejections[0].Reason != "script-syntax" {
		return ""
	}
	line := rejections[0].SourceLine
	trimmed := strings.TrimRight(script, " \t\r\n")
	if line < 1 || line != mekugi.TextLineCount(trimmed) {
		return ""
	}
	return fmt.Sprintf("\nInvalid final input at script line %d; no effects were applied. Remove or correct it through functions.hpatch_recover.\n", line)
}

func genericRecoveryGuidance(script string, rejections []mekugi.HostRejection, refreshed bool, handles []string) string {
	var output strings.Builder
	if refreshed {
		output.WriteString("\nThis re-rejection changed no workspace file. Corrections are retained only in the new rejected-script baseline; earlier script rows and command handles may be stale.\n")
	}
	output.WriteString("\nRepair retained-script text with ordinary type/add mutations through functions.hpatch_recover, without in/new/mv/rm commands. Targets below address the rejected script, not workspace files. Use exact known literals for other retained text. The router rebuilds and reevaluates the complete script atomically; do not repeat unrelated prepared edits.\n\nRetained rejected-script rows:\n")
	lines := hpatchsyntax.SplitPhysicalLines(script)
	logicalRows := mekugiLogicalRowsByPhysicalLine(script, lines)
	commands := recoveryCommands(script, handles)
	offered := make(map[int]bool)
	for _, rejection := range rejections {
		if rejection.Command < 1 || rejection.Command > len(commands) || offered[rejection.Command] {
			continue
		}
		command := commands[rejection.Command-1]
		if !command.parts.parsed {
			continue
		}
		offered[rejection.Command] = true
		fmt.Fprintf(&output, "Command correction: %s value VALUE replaces only this command's value (quoted or heredoc).\n", command.handle)
		if command.parts.target != "" && command.parts.target != "EOF" {
			fmt.Fprintf(&output, "Command correction: %s target TARGET replaces only its workspace target.\n", command.handle)
		}
	}
	if len(offered) != 0 {
		output.WriteString("Use each handle once; command corrections may share a payload but cannot mix with script-text mutations. Generated-source line numbers are diagnostic only, never recovery targets.\n\n")
	}
	const rowLimit = 12
	rows := make([]int, 0, rowLimit)
	seen := make(map[int]bool)
	addPhysical := func(index int) {
		if index < 0 || index >= len(logicalRows) {
			return
		}
		for _, row := range logicalRows[index] {
			if !seen[row] && len(rows) < rowLimit {
				rows = append(rows, row)
				seen[row] = true
			}
		}
	}
	for _, rejection := range rejections {
		if rejection.Command < 1 || rejection.Command > len(commands) {
			continue
		}
		command := commands[rejection.Command-1]
		addPhysical(command.header)
		if rejection.ValueLine > 0 {
			for _, offset := range []int{-1, 0, 1} {
				addPhysical(command.header + rejection.ValueLine + offset)
			}
		} else {
			addPhysical(command.header + 1)
			addPhysical(command.end - 1)
		}
	}
	if len(rows) == 0 {
		for index := len(logicalRows) - 1; index >= 0; index-- {
			addPhysical(index)
			if len(rows) != 0 {
				break
			}
		}
	}
	slices.Sort(rows)
	output.WriteString(mekugi.TextReferences(script, rows...))
	if len(rows) == rowLimit {
		output.WriteString("Preview limited to 12 script rows.\n")
	}
	return output.String()
}

func mekugiRecoveryReferences(
	script string,
	rejections []mekugi.HostRejection,
	refreshed bool,
	handles []string,
) (string, bool) {
	commands := recoveryCommands(script, handles)
	relevant := make(map[int]struct{})
	for _, rejection := range rejections {
		if rejection.Reason != "row-stale" || rejection.Command < 1 || rejection.Command > len(commands) {
			return "", false
		}
		command := commands[rejection.Command-1]
		if !command.parts.parsed || command.parts.target == "" {
			return "", false
		}
		relevant[rejection.Command] = struct{}{}
	}
	if len(relevant) == 0 {
		return "", false
	}

	var output strings.Builder
	if refreshed {
		output.WriteString("This re-rejection changed no workspace file. Earlier command handles are stale; use only the current handles below.\n\n")
	}
	output.WriteString("Rejected target commands:\n")
	indices := make([]int, 0, len(relevant))
	for index := range relevant {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	for _, index := range indices {
		command := commands[index-1]
		fmt.Fprintf(&output, "    %s %s\n", command.handle, mekugiRecoveryCommandSummary(command))
	}
	output.WriteString("\nSend one line per listed command as HANDLE CURRENT_TARGET. Put all corrections in one functions.hpatch_recover payload; the router preserves every operation and value and reevaluates the complete script.\n")
	return output.String(), true
}

// mekugiRecoveryCommandSummary creates a summary string for a recovery command reference.
func mekugiRecoveryCommandSummary(command recoveryCommandReference) string {
	summary := command.parts.operation
	if command.parts.target != "" {
		summary += " " + command.parts.target
	}
	if command.parts.multiline {
		return summary + " [heredoc value]"
	}
	return summary + " [inline value]"
}

func mekugiLogicalRowsByPhysicalLine(script string, lines []hpatchsyntax.PhysicalLine) [][]int {
	mapped := make([][]int, len(lines))
	offset := 0
	logicalRow := 1
	for index, line := range lines {
		next := offset + len(line.Text) + len(line.Terminator)
		count := mekugi.TextLineCount(script[offset:next])
		for range count {
			mapped[index] = append(mapped[index], logicalRow)
			logicalRow++
		}
		offset = next
	}
	return mapped
}

type mekugiOutcomeReporter interface {
	ReportOutcome(ctx context.Context, stage, outcome string) error
}

// recoveryHistoryOf picks the newest recoverable call. Mixed scripts are
// eligible only when preflight failed before retaining an executable carrier.
func recoveryHistoryOf(histories iter.Seq[mekugiHistory]) (mekugiHistory, error) {
	var latest mekugiHistory
	found := false
	for history := range histories {
		if history.Unevaluated || (history.ToolName != mekugiToolName && history.ToolName != mekugiRecoveryToolName) {
			continue
		}
		if !found || history.sequence > latest.sequence {
			latest = history
			found = true
		}
	}
	if !found {
		return mekugiHistory{}, errors.New("no rejected HPATCH script to recover; send a complete script")
	}
	baseline := latest.recoveryBaseline()
	isResume := strings.HasPrefix(strings.TrimSpace(baseline), "resume ")
	if _, mixed, _ := hpatchsyntax.SplitShell(baseline); mixed || isResume {
		// A carrier means preflight succeeded and execution may have started.
		if latest.TranslationError == "" && latest.CarrierPayload != "" {
			return latest, errors.New("mixed HPATCH/shell work uses retained continuation, not rejected-script recovery; inspect its checkpoints, current files, and known sessions, then use hpatch with resume HANDLE; successful preflight does not confirm execution; never resend the complete original script")
		}
		if !isResume && latest.TranslationError != "" {
			if latest.RecoveryBinding != recoveryHandlesBinding(baseline, nil) {
				return latest, errors.New("retained recovery binding does not match the mixed preflight baseline")
			}
			return latest, nil
		}
		if !isResume {
			return latest, errors.New("mixed HPATCH/shell state is not recoverable; inspect its checkpoints, current files, and known sessions")
		}
		return latest, errors.New("the resume request was rejected before execution; correct its diagnostic and inspect the original continuation handle, checkpoints, current files, and known sessions; do not resend the original mixed script or use rejected-script recovery")
	}
	if latest.TranslationError == "" {
		return latest, errors.New("the most recent mekugi call succeeded; recovery edits require a rejected script, so send a complete script")
	}
	if !latest.EvaluatorRejected {
		return latest, errors.New("the most recent mekugi call did not produce an evaluator rejection; send a complete script")
	}
	if latest.RecoveryBinding != recoveryHandlesBinding(baseline, latest.RecoveryHandles) {
		return latest, errors.New("retained recovery handle binding does not match the rejected baseline")
	}
	return latest, nil
}

func latestRecoveryAttempt(histories iter.Seq[mekugiHistory], correlationID string) int {
	latest := 0
	for history := range histories {
		if (history.ToolName == mekugiToolName || history.ToolName == mekugiRecoveryToolName) && history.CorrelationID == correlationID {
			latest = max(latest, history.Attempt)
		}
	}
	return latest
}

// recoveryBaseline is the complete rejected script a following recovery edits.
func (h mekugiHistory) recoveryBaseline() string {
	if h.Evaluated != "" {
		return h.Evaluated
	}
	return h.Script
}

func (t *mekugiResponseTransform) translateRecovery(
	callID, input string,
	upstreamItem map[string]json.RawMessage,
) (mekugiHistory, error) {
	if history, ok := t.local[callID]; ok {
		if history.ToolName != mekugiRecoveryToolName || history.PluginID != "" || history.Script != input {
			return mekugiHistory{}, fmt.Errorf("mekugi recovery call %q changed input", callID)
		}
		if len(upstreamItem) != 0 {
			history.UpstreamItem = maps.Clone(upstreamItem)
			t.local[callID] = history
		}
		return history, nil
	}
	if len(input) > maxMekugiScriptBytes {
		return mekugiHistory{}, fmt.Errorf("mekugi recovery call %q payload exceeds %d bytes", callID, maxMekugiScriptBytes)
	}
	base, baseErr := t.recoveryHistory()
	attemptMetadata := mekugi.AttemptMetadata{
		SessionID:      t.sessionID,
		Title:          t.proxy.titles.title(t.sessionID),
		CorrelationID:  callID,
		CallID:         callID,
		Attempt:        1,
		Correction:     true,
		Model:          t.model,
		ToolName:       mekugiRecoveryToolName,
		EmittedPayload: input,
	}
	if base.CorrelationID != "" {
		attemptMetadata.CorrelationID = base.CorrelationID
		attemptMetadata.Attempt = t.nextRecoveryAttempt(base.CorrelationID, base.Attempt)
	}
	if baseErr != nil {
		return t.rejectUnevaluated(mekugiRecoveryToolName, callID, input, baseErr, attemptMetadata, "", nil, upstreamItem, nil)
	}
	if base.Root != t.directory {
		return t.rejectUnevaluated(
			mekugiRecoveryToolName,
			callID,
			input,
			errors.New("the rejected script belongs to a different worktree; send a complete script"),
			attemptMetadata,
			"",
			nil,
			upstreamItem,
			nil,
		)
	}
	baseline := base.recoveryBaseline()
	recovered, err := recoverScriptDetailed(t.ctx, baseline, input, base.RecoveryHandles)
	if err != nil {
		return t.rejectUnevaluated(
			mekugiRecoveryToolName,
			callID,
			input,
			err,
			attemptMetadata,
			baseline,
			base.Rejections,
			upstreamItem,
			base.RecoveryHandles,
		)
	}
	attemptMetadata.EvaluatedScript = recovered.script
	attemptMetadata.RecoveryDelta = recovered.delta
	if parts, mixed, splitErr := hpatchsyntax.SplitShell(recovered.script); mixed {
		return t.translateMixedAttempt(callID, input, recovered.script, parts, splitErr, attemptMetadata, upstreamItem)
	}
	return t.evaluateScript(callID, input, recovered.script, attemptMetadata, upstreamItem)
}

func (t *mekugiResponseTransform) rejectUnevaluated(
	toolName, callID, input string,
	rejection error,
	attempt mekugi.AttemptMetadata,
	referenceScript string,
	rejections []mekugi.HostRejection,
	upstreamItem map[string]json.RawMessage,
	handles []string,
) (mekugiHistory, error) {
	changeID, err := t.changeIDForAttempt(attempt)
	if err != nil {
		return mekugiHistory{}, err
	}
	diagnostic := changeNotice(changeID) + rejection.Error()
	if referenceScript != "" {
		diagnostic += mekugiRecoveryGuidance(referenceScript, rejections, false, handles)
	}
	if reporter, ok := t.proxy.translator.(mekugiOutcomeReporter); ok {
		attemptContext := mekugi.WithAttemptMetadata(t.ctx, attempt)
		if hookErr := reporter.ReportOutcome(attemptContext, "unevaluated", "rejected"); hookErr != nil {
			diagnostic += "\nmekugi: warning: " + strings.TrimSpace(hookErr.Error()) + "\n"
		}
	}
	history := mekugiHistory{
		ToolName: toolName,
		Script:   input,

		Root:             t.directory,
		ChangeID:         changeID,
		CarrierName:      t.codeModeToolName,
		TranslationError: diagnostic,
		CorrelationID:    attempt.CorrelationID,
		Attempt:          attempt.Attempt,
		UpstreamItem:     maps.Clone(upstreamItem),
		Unevaluated:      true,
	}
	t.recordLocal(callID, &history)
	return history, nil
}

// recoveryHistory is the rejected call a recovery in this turn edits. A
// rejection this turn is newer than retained history, which commits only after
// the response completes.
func (t *mekugiResponseTransform) recoveryHistory() (mekugiHistory, error) {
	for _, history := range t.local {
		if !history.Unevaluated &&
			(history.ToolName == mekugiToolName || history.ToolName == mekugiRecoveryToolName) {
			return recoveryHistoryOf(maps.Values(t.local))
		}
	}
	return recoveryHistoryOf(maps.Values(t.visible))
}

func (t *mekugiResponseTransform) nextRecoveryAttempt(correlationID string, baseAttempt int) int {
	latest := max(baseAttempt, latestRecoveryAttempt(maps.Values(t.visible), correlationID))
	latest = max(latest, latestRecoveryAttempt(maps.Values(t.local), correlationID))
	return max(latest+1, 2)
}
