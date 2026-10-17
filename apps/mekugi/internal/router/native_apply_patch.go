package router

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func nativePatchesInCall(name, input, workspace string) []changecapture.PatchObservation {
	var patches []string
	if name == applyPatchToolName {
		patches = []string{input}
	} else if name == "exec" {
		patches = binding.LiteralPatchInputs(input)
	}
	return changecapture.Patches(patches, workspace)
}

func (p *mekugiProxy) finalizeNativePatches(ctx context.Context, workspace, thread, callID string, history mekugiHistory, result binding.ExecutionResult) error {
	if len(history.NativePatches) == 0 {
		return nil
	}
	if !result.Terminal {
		return nil
	}
	windowFiles := changecapture.PatchWindowFiles(history.NativePatches)
	occurrences := make(map[string]int)
	for index, observation := range history.NativePatches {
		occurrence := occurrences[observation.Input]
		occurrences[observation.Input]++
		if history.nativeCell.PatchPending(observation.Input, occurrence) {
			continue // Outer completion cannot finish a still-running nested edit.
		}
		derivedCallID := legacycall.PatchCallID(callID, index)
		if retained, found, err := p.retained.Store.LookupHostCall(ctx, workspace, derivedCallID); err != nil {
			return err
		} else if found {
			if retained.CorrelationID != callID+"\x00"+strconv.Itoa(index) || retained.Script != observation.Input {
				return errors.New("retained stock apply_patch observation is inconsistent")
			}
			continue
		}
		correlation := callID + "\x00" + strconv.Itoa(index)
		reviews, complete := changecapture.ReviewPatch(windowFiles[index])
		// Attempts remain durable, but IDs select actual or incomplete file
		// evidence, not successful no-ops or rejected patches with no effects.
		changeID := ""
		if len(reviews) != 0 && p.retained.Store != nil {
			var err error
			changeID, err = p.changeSource.Reserve(ctx, observationScope(ctx, p.retained), workspace, thread, correlation)
			if err != nil {
				return changeError(err)
			}
		}
		success := history.ToolName == applyPatchToolName && result.Succeeded && complete
		nested, confirmed := history.nativeCell.Patch(observation.Input, occurrence)
		if confirmed {
			success = nested.Status == "completed" && complete
		}
		attempt := mekugiHistory{
			ToolName:         applyPatchToolName,
			Script:           observation.Input,
			Root:             workspace,
			ExecutingThread:  thread,
			Caller:           history.Caller,
			ChangeID:         changeID,
			CorrelationID:    correlation,
			Attempt:          1,
			AlreadySatisfied: success && len(reviews) == 0 && len(history.NativePatches) == 1,
			ReviewFiles:      reviews,
			Report:           result.Text,
			CarrierKind:      codeModeCarrierCustom,
			CarrierName:      applyPatchToolName,
			CarrierPayload:   observation.Input,
			ReplayCarrier:    true,
			UpstreamItem: map[string]json.RawMessage{
				"type":    responses.MustMarshalJSON("custom_tool_call"),
				"name":    responses.MustMarshalJSON(applyPatchToolName),
				"call_id": responses.MustMarshalJSON(derivedCallID),
				"input":   responses.MustMarshalJSON(observation.Input),
				"status":  responses.MustMarshalJSON("completed"),
			},
		}
		if len(history.NativePatches) > 1 {
			attempt.Report += "\nFile evidence spans the exec cell; shared paths are recorded once at their first patch observation, not as per-patch effects."
		}
		if confirmed {
			attempt.HostResults = []binding.ToolResult{nested}
			if nested.Status != "completed" {
				attempt.TranslationError = "host tool " + nested.CallID + " " + nested.Tool + ": " + nested.Status
				if nested.ExitCode != nil {
					attempt.TranslationError += "; exit " + strconv.Itoa(*nested.ExitCode)
				}
			}
		}
		if history.ToolName == applyPatchToolName && !result.Succeeded {
			attempt.TranslationError = strings.TrimSpace(result.Text)
			if attempt.TranslationError == "" {
				attempt.TranslationError = "stock apply_patch failed"
			}
		}
		if !complete && attempt.Report != "" {
			attempt.Report += "\nMekugi could not capture complete file evidence."
		}
		if err := publishObservations(context.WithoutCancel(ctx), p.retained, p.changeSource, p.publishChanges, workspace, map[string]mekugiHistory{derivedCallID: attempt}); err != nil {
			return changeError(err)
		}
		if len(reviews) != 0 {
			_ = publishEditReceipt(context.WithoutCancel(ctx), p.retained, p.changeSource, workspace, thread, derivedCallID, p.activity, responses.StringField(history.UpstreamItem, "id"))
		}
	}
	return nil
}
