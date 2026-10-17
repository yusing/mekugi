package router

import (
	"cmp"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func (t *mekugiResponseTransform) TransformJSON(payload []byte) ([]byte, error) {
	transformed, err := t.transformResponse(payload, "")
	return transformed, translationDiagnostic(err, "mekugi_json", "Mekugi response translation failed while processing a JSON response")
}

func (t *mekugiResponseTransform) Finish(bool) error {
	return nil
}

func (t *mekugiResponseTransform) TransformSSE(payload []byte) ([][]byte, error) {
	visible, err := t.transformActivitySSE(payload)
	return visible, translationDiagnostic(err, "mekugi_sse", "Mekugi response translation failed while processing an upstream streaming event")
}

func translationDiagnostic(err error, code, summary string) error {
	if err == nil {
		return nil
	}
	if inner, ok := errors.AsType[*requestexecution.DiagnosticError](err); ok {
		code += ":" + inner.Code
		summary = inner.Summary
	}
	return &requestexecution.DiagnosticError{Cause: err, Code: code, Summary: summary, Distinct: true}
}

func (t *mekugiResponseTransform) transformActivitySSE(payload []byte) ([][]byte, error) {
	var envelope struct {
		Type      responseevents.Kind `json:"type"`
		ItemID    string              `json:"item_id"`
		CallID    string              `json:"call_id"`
		Name      string              `json:"name"`
		Delta     string              `json:"delta"`
		Input     string              `json:"input"`
		Arguments string              `json:"arguments"`
		Item      json.RawMessage     `json:"item"`
		Response  json.RawMessage     `json:"response"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.finalAnswer.observe(payload)
		return [][]byte{payload}, nil
	}
	t.finalAnswer.observe(payload)
	switch {
	case envelope.Type == responseevents.OutputItemAdded:
		item, ok := responseevents.DecodeItem(envelope.Item)
		if !ok {
			return [][]byte{payload}, nil //nolint:nilerr // Unrelated output items pass through unchanged.
		}
		if item.Type == "function_call" || item.Type == "custom_tool_call" {
			if t.proxy != nil {
				t.proxy.control.SetToolInput(&t.generation, t.directory, t.threadID, item.ID, true)
			}
		}
		name := item.Name
		if t.codeModeToolName != "" && name == t.codeModeToolName {
			if item.Type == "custom_tool_call" && item.ID != "" {
				t.nativeExecCalls[item.ID] = item.CloneFields()
				if item.Input != nil {
					t.previewStockDelta(item.ID, name, *item.Input)
				}
			} else if item.Type == "function_call" && item.ID != "" {
				t.nativeExecCalls[item.ID] = item.CloneFields()
			}
			return [][]byte{payload}, nil
		}
		return [][]byte{payload}, nil

	case envelope.Type == responseevents.CustomInputDelta:
		if fields := t.nativeExecCalls[envelope.ItemID]; fields != nil {
			t.previewStockDelta(envelope.ItemID, responseevents.StringField(fields, "name"), envelope.Delta)
		}
		return [][]byte{payload}, nil

	case envelope.Type == responseevents.CustomInputDone:
		if t.proxy != nil {
			t.proxy.control.SetToolInput(&t.generation, t.directory, t.threadID, envelope.ItemID, false)
		}
		if addedFields, stockCall := t.nativeExecCalls[envelope.ItemID]; stockCall {
			if responseevents.StringField(addedFields, "type") == "custom_tool_call" {
				addedCallID := responseevents.StringField(addedFields, "call_id")
				if addedCallID != "" && envelope.CallID != "" && addedCallID != envelope.CallID {
					return nil, requestexecution.StaticError("changed_code_mode_call", "the upstream changed a exec call identity")
				}
				callID := cmp.Or(addedCallID, envelope.CallID)
				t.finishPreview(envelope.ItemID, envelope.Input)
				defer delete(t.previews, envelope.ItemID)
				addedFields["call_id"] = responseevents.MustMarshalJSON(callID)
				original := maps.Clone(addedFields)
				original["input"] = responseevents.MustMarshalJSON(envelope.Input)
				item := responseevents.NewItem(original)
				changed, err := t.transformOutputItem(&item)
				if err != nil {
					return nil, err
				}
				event := payload
				if changed {
					event, err = responseevents.ReplaceField(payload, "input", item.Fields["input"])
					if err != nil {
						return nil, err
					}
				}
				if err := t.commitLocalCall(callID); err != nil {
					return nil, err
				}
				return [][]byte{event}, nil
			}
		}
		return [][]byte{payload}, nil

	case envelope.Type == responseevents.FunctionArgumentsDone:
		if t.proxy != nil {
			t.proxy.control.SetToolInput(&t.generation, t.directory, t.threadID, envelope.ItemID, false)
		}
		return [][]byte{payload}, nil

	case envelope.Type == responseevents.OutputItemDone:
		item, ok := responseevents.DecodeItem(envelope.Item)
		if !ok {
			return [][]byte{payload}, nil //nolint:nilerr // Malformed unrelated output remains the upstream's responsibility.
		}
		t.storageToolHandoff = t.storageToolHandoff || blocksTokenUsage(item.Fields)
		if t.proxy != nil {
			t.proxy.control.SetToolInput(&t.generation, t.directory, t.threadID, item.ID, false)
		}
		if _, delivered := t.local[item.CallID]; item.Status == "incomplete" && !delivered {
			t.endPreview(item.ID)
			// Item completion can report interrupted generation, not complete input.
			delete(t.nativeExecCalls, item.ID)
			return [][]byte{payload}, nil
		}
		itemID := item.ID
		callID := item.CallID
		if addedFields, stockCall := t.nativeExecCalls[itemID]; stockCall {
			expectedCallID := responseevents.StringField(addedFields, "call_id")
			expectedType, expectedName := responseevents.StringField(addedFields, "type"), responseevents.StringField(addedFields, "name")
			if item.Type != expectedType || item.Name != expectedName || expectedCallID != "" && expectedCallID != callID {
				t.endPreview(item.ID)
				return nil, requestexecution.StaticError("inconsistent_stock_call", "the upstream completed an inconsistent stock tool call")
			}
		}
		if item.Input != nil {
			t.finishPreview(item.ID, *item.Input)
		} else if item.Arguments != nil {
			t.finishPreview(item.ID, *item.Arguments)
		} else {
			t.endPreview(item.ID)
		}
		defer delete(t.previews, item.ID)
		delete(t.nativeExecCalls, itemID)
		changed, err := t.transformOutputItem(&item)
		if err != nil {
			return nil, requestexecution.Classify(err, "output_item_projection", "Mekugi could not project a completed upstream output item", true)
		}
		if err := t.commitLocalCall(callID); err != nil {
			return nil, err
		}
		if !changed {
			return [][]byte{payload}, nil
		}
		transformed, err := responseevents.MarshalJSON(item)
		if err != nil {
			return nil, requestexecution.Classify(err, "output_item_encode", "Mekugi could not encode a completed output item", true)
		}
		event, err := responseevents.ReplaceField(payload, "item", transformed)
		if err != nil {
			return nil, requestexecution.Classify(err, "output_item_event_projection", "Mekugi could not project a completed output-item event", true)
		}
		return [][]byte{event}, nil

	case envelope.Type.Terminal():
		clear(t.nativeExecCalls)
		transformed, err := t.transformResponse(envelope.Response, envelope.Type.Status())
		if err != nil {
			return nil, err
		}
		event, err := responseevents.ReplaceField(payload, "response", transformed)
		if err != nil {
			return nil, err
		}
		var terminal struct {
			Status string `json:"status"`
		}
		if envelope.Type == responseevents.Completed && json.Unmarshal(transformed, &terminal) == nil && terminal.Status == "failed" {
			event, err = responseevents.ReplaceField(event, "type", responseevents.MustMarshalJSON(responseevents.Failed))
		}
		if err != nil {
			return nil, err
		}
		var completed [][]byte
		if envelope.Type == responseevents.Completed {
			completed, err = t.finalAnswer.complete(transformed)
			if err != nil {
				return nil, err
			}
		} else {
			clear(t.finalAnswer.pending)
		}
		return append(completed, event), nil

	default:
		return [][]byte{payload}, nil
	}
}

func (t *mekugiResponseTransform) transformResponse(payload []byte, terminalStatus string) ([]byte, error) {
	var object map[string]json.RawMessage
	var err error
	err = json.Unmarshal(payload, &object)
	if err == nil && object == nil {
		err = errors.New("decode mekugi-enabled response")
	}

	if err != nil {
		return nil, err
	}
	// SSE terminal events own completion even when the embedded status is absent.
	// JSON responses have no event envelope and retain body-status semantics.
	status := cmp.Or(terminalStatus, responseevents.StringField(object, "status"))
	interrupted := status == "failed" || status == "incomplete"
	if rawOutput, ok := object["output"]; ok {
		var output []map[string]json.RawMessage
		if err := json.Unmarshal(rawOutput, &output); err != nil {
			return nil, errors.New("decode mekugi-enabled response output")
		}
		transformedOutput := make([]map[string]json.RawMessage, 0, len(output))
		for _, fields := range output {
			t.storageToolHandoff = t.storageToolHandoff || blocksTokenUsage(fields)
			t.finalAnswer.substantive = t.finalAnswer.substantive || isSubstantiveAnswer(fields)
			t.finalAnswer.blocked = t.finalAnswer.blocked || blocksTokenUsage(fields)
			item := responseevents.NewItem(fields)
			// An interrupted response can contain partial calls. Only complete items
			// or calls whose complete input was already delivered may be projected.
			_, delivered := t.local[item.CallID]
			if !delivered && (interrupted && item.Status != "completed" || item.Status == "in_progress" || item.Status == "incomplete") {
				transformedOutput = append(transformedOutput, fields)
				continue
			}
			if _, err := t.transformOutputItem(&item); err != nil {
				return nil, err
			}
			transformedOutput = append(transformedOutput, item.Fields)
		}
		encoded, err := responseevents.MarshalJSON(transformedOutput)
		if err != nil {
			return nil, err
		}
		object["output"] = encoded
	}
	t.responseContract.Restore(object)
	// Every translated carrier in a JSON body is about to become visible,
	// regardless of whether the provider supplied a terminal response status.
	if err := t.commitHistory(); err != nil {
		return nil, err
	}
	transformed, err := responseevents.MarshalJSON(object)
	if err == nil {
		t.storageCompletionReady = status == "completed" && !t.storageToolHandoff
	}
	if err == nil && !t.subagentTurn && status == "completed" && t.finalAnswer.substantive && !t.finalAnswer.blocked && !t.finalAnswer.disabled {
		t.liveDiffCompletionReady = true
	}
	return transformed, err
}

func (t *mekugiResponseTransform) transformOutputItem(item *responseevents.Item) (bool, error) {
	name := item.Name
	if t.codeModeToolName != "" && name == t.codeModeToolName &&
		item.Type == "custom_tool_call" {
		callID := item.CallID
		if callID == "" {
			return false, requestexecution.StaticError("code_mode_call_missing_id", "the upstream exec call had no call ID")
		}
		var originalInput string
		if item.Input != nil {
			originalInput = *item.Input
		}
		if retained, exists := t.local[callID]; exists && retained.ToolName == name {
			if retained.Script != originalInput {
				return false, requestexecution.Classify(fmt.Errorf("exec call %q changed input", callID),
					"code_mode_call_input_changed", "the completed exec call changed its earlier input", true)
			}
			retained.UpstreamItem = item.CloneFields()
			t.local[callID] = retained
			item.SetInput(retained.CarrierPayload)
			return retained.CarrierPayload != originalInput, nil
		}
		patches := nativePatchesInCall(name, originalInput, t.directory)
		t.primePreviewSources(item.ID, patches)
		execs, dynamic := commandanalysis.LiteralExecCommands(originalInput, t.directory, t.sessionShell)
		// Source-resolved edit operands acquire baselines; a cell that may write
		// elsewhere, including a dynamic one, is bounded by a workspace snapshot.
		observation, observed := changecapture.Capture(t.ctx, execs, dynamic, true, executionCaptureEnvironment(t.ctx, t.directory, t.proxy.replayStore, patches))
		previewOpened := t.openStockExecWindow(callID, observation, patches, execs, dynamic, true)
		if len(patches) == 0 && !observed && !previewOpened {
			return false, nil
		}
		if observation == nil && len(execs) != 0 {
			// Retained patch cells still need literal reader identities
			// for host-trace matching, without a baseline or writer window.
			observation = &changecapture.Observation{Commands: execs, Class: execscope.Neutral.String(), CodeMode: true}
		}
		// Retain the provider input and captured baseline before exposing any
		// executable call. The stock host remains the only edit executor.
		history := mekugiHistory{
			ToolName: name,
			Script:   originalInput, CarrierKind: codeModeCarrierCustom,
			CarrierName: name, CarrierPayload: originalInput, UpstreamItem: item.CloneFields(),
			ReplayCarrier:   true,
			NativePatches:   patches,
			ExecObservation: observation,
			ExecutingThread: t.shellThreadID,
			Caller:          t.operationCaller(),
		}
		t.recordLocal(callID, &history)
		return false, nil
	}
	return false, nil
}

func (t *mekugiResponseTransform) openExecWindow(callID string, observation *changecapture.Observation, patches []changecapture.PatchObservation) {
	if observation == nil && len(patches) == 0 || t.proxy == nil {
		return
	}
	if t.execGroup == "" {
		t.execGroup = rand.Text()
	}
	window := &execWindow{ref: callID, thread: t.shellThreadID, turn: t.shellTurnID, group: t.execGroup}
	admitExecutionWindow(t.proxy.execWindows, t.proxy.review, window, observation, patches, executionCaptureEnvironment(t.ctx, t.directory, t.proxy.replayStore, patches), t.threadID, t.operationCaller())
}

// Display baselines are process-local. They preserve running LiveDiff without
// turning formatter/managed effects into persisted authored changes.
func (t *mekugiResponseTransform) openStockExecWindow(callID string, authored *changecapture.Observation, patches []changecapture.PatchObservation, commands []commandanalysis.Command, dynamic, codeMode bool) bool {
	display := authored
	if (authored == nil || authored.Class == execscope.Opaque.String()) && t.proxy != nil && t.proxy.review.Preview().Enabled() {
		env := executionCaptureEnvironment(t.ctx, t.directory, t.proxy.replayStore, patches)
		env.PreviewOnly = true
		if preview, ok := changecapture.Capture(t.ctx, commands, dynamic, codeMode, env); ok {
			if authored != nil {
				preview.Tree = authored.Tree
			}
			display = preview
		}
	}
	t.openExecWindow(callID, display, patches)
	if authored != nil && display != nil {
		authored.Group = display.Group
	}
	return display != nil
}
