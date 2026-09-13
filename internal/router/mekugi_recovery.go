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
) string {
	references, eligible := mekugiRecoveryReferences(script, rejections, refreshed)
	if !eligible {
		return genericRecoveryGuidance(script, rejections, refreshed)
	}
	return codexinstructions.RecoveryGuidance(references)
}

func genericRecoveryGuidance(script string, rejections []mekugi.HostRejection, refreshed bool) string {
	var output strings.Builder
	if refreshed {
		output.WriteString("\nThis re-rejection changed no workspace file. Corrections are retained only in the new rejected-script baseline; earlier script rows and C... handles may be stale.\n")
	}
	output.WriteString("\nRepair retained-script text with ordinary type/add mutations through functions.hpatch_recover, without in/new/mv/rm commands. Targets below address the rejected script, not workspace files. Use exact known literals for other retained text. The router rebuilds and reevaluates the complete script atomically; do not repeat unrelated prepared edits.\n\nRetained rejected-script rows:\n")
	lines := hpatchsyntax.SplitPhysicalLines(script)
	logicalRows := mekugiLogicalRowsByPhysicalLine(script, lines)
	commands := recoveryCommands(script)
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
) (string, bool) {
	commands := recoveryCommands(script)
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
		output.WriteString("This re-rejection changed no workspace file. Earlier C... handles are stale; use only the current handles below.\n\n")
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
	output.WriteString("\nSend one line per listed command as C... CURRENT_TARGET. Put all corrections in one functions.hpatch_recover payload; the router preserves every operation and value and reevaluates the complete script.\n")
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

// recoveryHistoryOf picks the newest call that mekugi actually evaluated.
// Proxy-rejected calls are skipped because they changed nothing. A successful
// newest call blocks recovery of an older rejection.
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
	isResume := strings.HasPrefix(strings.TrimSpace(latest.Script), "resume ")
	if _, mixed, _ := hpatchsyntax.SplitShell(latest.Script); mixed || isResume {
		// Successful mixed translation records a carrier only after retention.
		// Native preflight rejections can also have diagnostic carriers.
		if latest.TranslationError == "" && latest.CarrierPayload != "" {
			return latest, errors.New("mixed HPATCH/shell work uses retained continuation, not edit-only recovery; inspect its checkpoints, current files, and known sessions, then use hpatch with resume HANDLE; successful preflight does not confirm execution; never resend the complete original script")
		}
		if !isResume {
			return latest, errors.New("mixed HPATCH/shell preflight failed before a continuation handle was retained; no segment ran; correct the preflight error and submit the corrected script through hpatch, not hpatch_recover")
		}
		return latest, errors.New("the resume request was rejected before execution; correct its diagnostic and inspect the original continuation handle, checkpoints, current files, and known sessions; do not resend the original mixed script or use edit-only recovery")
	}
	if latest.TranslationError == "" {
		return latest, errors.New("the most recent mekugi call succeeded; recovery edits require a rejected script, so send a complete script")
	}
	if !latest.EvaluatorRejected {
		return latest, errors.New("the most recent mekugi call did not produce an evaluator rejection; send a complete script")
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
		return t.rejectUnevaluated(mekugiRecoveryToolName, callID, input, baseErr, attemptMetadata, "", nil, upstreamItem)
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
		)
	}
	baseline := base.recoveryBaseline()
	recovered, err := recoverScriptDetailed(t.ctx, baseline, input)
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
		)
	}
	attemptMetadata.EvaluatedScript = recovered.script
	attemptMetadata.RecoveryDelta = recovered.delta
	return t.evaluateScript(callID, input, recovered.script, attemptMetadata, upstreamItem)
}

func (t *mekugiResponseTransform) rejectUnevaluated(
	toolName, callID, input string,
	rejection error,
	attempt mekugi.AttemptMetadata,
	referenceScript string,
	rejections []mekugi.HostRejection,
	upstreamItem map[string]json.RawMessage,
) (mekugiHistory, error) {
	changeID, err := t.changeIDForAttempt(attempt)
	if err != nil {
		return mekugiHistory{}, err
	}
	diagnostic := changeNotice(changeID) + rejection.Error()
	if referenceScript != "" {
		diagnostic += mekugiRecoveryGuidance(referenceScript, rejections, false)
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
