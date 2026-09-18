package router

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/hpatchsyntax"
)

type hpatchTranslation struct {
	Patch      string `json:"patch"`
	Report     string `json:"report"`
	Diagnostic string `json:"diagnostic"`
	AttemptID  string `json:"attempt_id,omitempty"`
}

func translateHpatchSegment(ctx context.Context, directory, source string) (hpatchTranslation, []mekugi.ReviewFile, error) {
	result := hpatchTranslation{}
	if err := ctx.Err(); err != nil {
		return result, nil, err
	}
	if len(source) > maxMekugiScriptBytes {
		return result, nil, fmt.Errorf("HPATCH segment exceeds %d bytes", maxMekugiScriptBytes)
	}
	translated, err := mekugi.TranslateForHostAt(ctx, directory, source, "")
	if ctx.Err() != nil {
		return result, nil, ctx.Err()
	}
	if err != nil {
		result.Diagnostic = translated.Diagnostic
		if result.Diagnostic == "" {
			result.Diagnostic = err.Error()
		}
		return result, nil, nil
	}
	if len(translated.Patch) > maxMekugiPatchBytes {
		return result, nil, fmt.Errorf("HPATCH translation exceeds %d bytes", maxMekugiPatchBytes)
	}
	result.Patch = string(translated.Patch)
	result.Report = mekugiReport(translated.Report, translated.Diagnostic)
	return result, translated.ReviewFiles, nil
}

func (t *mekugiResponseTransform) translateMixedScript(callID, input string, parts []hpatchsyntax.ScriptSegment, splitErr error, upstreamItem map[string]json.RawMessage) (mekugiHistory, error) {
	attempt := mekugi.AttemptMetadata{
		CorrelationID:   callID,
		CallID:          callID,
		Attempt:         1,
		ToolName:        mekugiToolName,
		EmittedPayload:  input,
		EvaluatedScript: input,
	}
	return t.translateMixedAttempt(callID, input, input, parts, splitErr, attempt, upstreamItem)
}

func (t *mekugiResponseTransform) translateMixedAttempt(
	callID, input, evaluated string,
	parts []hpatchsyntax.ScriptSegment,
	splitErr error,
	attempt mekugi.AttemptMetadata,
	upstreamItem map[string]json.RawMessage,
) (mekugiHistory, error) {
	history := mekugiHistory{
		ToolName: attempt.ToolName, Script: input, Evaluated: retainedEvaluated(input, evaluated),
		Root: t.directory, CarrierName: t.codeModeToolName, UpstreamItem: maps.Clone(upstreamItem),
		CorrelationID: attempt.CorrelationID, Attempt: attempt.Attempt,
	}
	changeID, err := t.changeIDForAttempt(attempt)
	if err != nil {
		return mekugiHistory{}, err
	}
	history.ChangeID = changeID
	var segments []hpatchResumeSegment
	prepareErr := false
	err = splitErr
	if err == nil {
		segments, err = t.prepareMixedSegments(parts)
		prepareErr = err != nil
	}
	if err == nil {
		var state hpatchResumeState
		state, err = t.retainMixedScript(history.ChangeID, history.CorrelationID, evaluated, segments)
		if err == nil {
			history.CarrierKind = codeModeCarrierCustom
			history.CarrierPayload = t.mixedCarrier(state, "", nil)
		}
	}
	if err != nil {
		diagnostic := "hpatch: " + err.Error() + mixedPreflightFailureNotice(parts, prepareErr, err)
		diagnostic += genericRecoveryGuidance(evaluated, nil, attempt.Correction, nil)
		history.TranslationError = changeNotice(changeID) + diagnostic
		history.RecoveryBinding = recoveryHandlesBinding(evaluated, nil)
	}
	t.recordLocal(callID, &history)
	return history, nil
}

func mixedPreflightFailureNotice(parts []hpatchsyntax.ScriptSegment, prepareErr bool, err error) string {
	if prepareErr && strings.Contains(err.Error(), "unknown or malformed command") && len(parts) != 0 {
		last := parts[len(parts)-1]
		lines := hpatchsyntax.SplitPhysicalLines(last.Source)
		header := -1
		for index := 0; !last.Shell && index < len(lines); {
			if strings.TrimSpace(lines[index].Text) == "" {
				index++
				continue
			}
			header = index
			frame, _ := hpatchsyntax.FrameCommand(lines, index, lines[index].Text)
			index = max(index+1, frame.Next)
		}
		if header >= 0 {
			source := strings.TrimSpace(lines[header].Text)
			if syntaxErr := mekugi.ValidateScriptSyntax(source); syntaxErr != nil &&
				strings.Contains(syntaxErr.Error(), "unknown or malformed command") {
				return fmt.Sprintf("\nInvalid final input at script line %d; no effects were applied.", last.Line+header)
			}
		}
	}
	return "\nMixed-script preflight failed; no effects were applied."
}

func (t *mekugiResponseTransform) prepareMixedSegments(parts []hpatchsyntax.ScriptSegment) ([]hpatchResumeSegment, error) {
	if t.nativeTools {
		return nil, fmt.Errorf("shell-in-HPATCH requires Code Mode; submit separate hpatch and shell calls with this client")
	}
	contribution, ok := t.proxy.registry.contribution("shell")
	if !ok {
		return nil, fmt.Errorf("built-in shell tool is unavailable")
	}
	segments := make([]hpatchResumeSegment, 0, len(parts))
	for index, part := range parts {
		segment := hpatchResumeSegment{Source: part.Source, Line: part.Line, Kind: "edit"}
		var program strings.Builder
		if part.Shell {
			segment.Kind = "shell"
			// Empty shell programs complete without starting a host process.
			if strings.TrimSpace(part.Source) != "" {
				sources, translated, err := t.prepareShellBatch(contribution, []string{part.Source})
				if err != nil {
					return nil, fmt.Errorf("shell segment %d (line %d): %w", index+1, part.Line, err)
				}
				if translated.Rejected {
					return nil, fmt.Errorf("shell segment %d (line %d): %s", index+1, part.Line, translated.Diagnostic)
				}
				program.WriteString(sources[0])
			}
			program.WriteString("Object.assign(current, last, {output});\nif (last.exit_code !== 0) { current.status = 'failed'; stoppedReason = 'nonzero_exit'; return; }\n")
		} else {
			if err := mekugi.ValidateScriptSyntax(part.Source); err != nil {
				return nil, fmt.Errorf("edit segment %d (line %d): %w", index+1, part.Line, err)
			}
		}
		segment.Program = program.String()
		segments = append(segments, segment)
	}
	return segments, nil
}
