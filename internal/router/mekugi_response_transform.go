package router

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

func (t *mekugiResponseTransform) TransformJSON(payload []byte) ([]byte, error) {
	transformed, _, err := t.transformResponse(payload, "")
	if err == nil && t.journalActive {
		transformed, err = t.decorateJournalJSON(transformed)
	}
	return transformed, criticalDiagnostic(err, "mekugi_json", "Mekugi response translation failed while processing a JSON response", true)
}

func (t *mekugiResponseTransform) Finish(streamEvent bool) error {
	if streamEvent && len(t.pending) != 0 {
		return staticCriticalDiagnostic("stream_ended_incomplete_intercepted_call", "the upstream stream ended with an incomplete intercepted function call")
	}
	return nil
}

func (t *mekugiResponseTransform) TransformSSE(payload []byte) ([][]byte, error) {
	// Match the failed terminal projected to the host before journal interception
	// can prepare a successful flush or continue a failed response.
	var terminal struct {
		Type     string `json:"type"`
		Response struct {
			Status string `json:"status"`
		} `json:"response"`
	}
	if t.journalActive && json.Unmarshal(payload, &terminal) == nil && terminal.Type == responseevents.Completed && terminal.Response.Status == "failed" {
		var err error
		payload, err = replaceRawField(payload, "type", mustMarshalJSON(responseevents.Failed))
		if err != nil {
			return nil, err
		}
	}
	visible, err := t.transformSSE(payload)
	if err == nil && t.journalActive {
		visible, err = t.decorateJournalSSE(payload, visible)
	}
	return visible, criticalDiagnostic(err, "mekugi_sse", "Mekugi response translation failed while processing an upstream streaming event", true)
}

func (t *mekugiResponseTransform) transformSSE(payload []byte) ([][]byte, error) {
	var prefix [][]byte
	if t.journalActive {
		events, handled, err := t.interceptJournalSSE(payload)
		if handled || err != nil {
			return events, err
		}
		prefix = events
	}
	visible, err := t.transformNonJournalSSE(payload)
	return append(prefix, visible...), err
}

func (t *mekugiResponseTransform) transformNonJournalSSE(payload []byte) ([][]byte, error) {
	if len(t.subagentDeferred) != 0 {
		t.subagentDeferred = t.retainCommentary(t.subagentDeferred...)
		if len(t.subagentDeferred) == 0 {
			t.subagentResponses = nil
		}
	}
	messages := t.retainCommentary(t.drainActivity()...)
	t.activityMessages = append(t.activityMessages, messages...)
	visible, err := t.transformActivitySSE(payload)
	if err != nil || len(messages) == 0 {
		return visible, err
	}
	var generated [][]byte
	for _, message := range messages {
		generated = append(generated, assistantCommentaryDoneEvent(message))
	}
	var event struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(payload, &event)
	if event.Type == responseevents.Created && len(visible) != 0 {
		return append(append(visible[:1:1], generated...), visible[1:]...), nil
	}
	return append(generated, visible...), nil
}

func (t *mekugiResponseTransform) transformActivitySSE(payload []byte) ([][]byte, error) {
	var envelope struct {
		Type     responseevents.Kind `json:"type"`
		ItemID   string              `json:"item_id"`
		CallID   string              `json:"call_id"`
		Name     string              `json:"name"`
		Delta    string              `json:"delta"`
		Input    string              `json:"input"`
		Item     json.RawMessage     `json:"item"`
		Response json.RawMessage     `json:"response"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		if visible, buffered := t.finalAnswer.observe(payload); buffered {
			return visible, nil
		}
		if len(t.pending) != 0 {
			return nil, staticCriticalDiagnostic("malformed_pending_intercepted_event", "the upstream sent a malformed event while an intercepted function call was pending")
		}
		return [][]byte{payload}, nil
	}
	if envelope.Type == responseevents.OutputItemDone {
		var item map[string]json.RawMessage
		if json.Unmarshal(envelope.Item, &item) == nil {
			t.collectProviderCommentary(item)
		}
	}
	if visible, buffered := t.finalAnswer.observe(payload); buffered {
		return visible, nil
	}
	switch {
	case envelope.Type == responseevents.Created:
		visible := [][]byte{payload}
		for _, message := range t.subagentDeferred {
			visible = append(visible, assistantCommentaryDoneEvent(message))
			t.commentaryEmitted[jsonString(message, "id")] = struct{}{}
		}
		t.subagentDeferred = nil
		for _, publication := range t.deferredCommentary {
			if message := t.runtimeCommentaryMessage(publication); message != nil {
				visible = append(visible, assistantCommentaryDoneEvent(message))
			}
		}
		t.deferredCommentary = nil
		return visible, nil

	case envelope.Type == responseevents.OutputItemAdded:
		item, ok := decodeResponsesItem(envelope.Item)
		if !ok {
			return [][]byte{payload}, nil //nolint:nilerr // Unrelated output items pass through unchanged.
		}
		name := item.Name
		if t.codeModeToolName != "" && name == t.codeModeToolName ||
			t.nativeTools && (item.Type == "custom_tool_call" && name == applyPatchToolName ||
				item.Type == "function_call" && name == nativeExecCommandToolName) {
			if item.Type == "custom_tool_call" && item.ID != "" {
				t.nativeExecCalls[item.ID] = item.cloneFields()
				if item.Input != nil {
					t.previewStockDelta(item.ID, name, *item.Input)
				}
			} else if item.Type == "function_call" && item.ID != "" {
				t.nativeExecCalls[item.ID] = item.cloneFields()
			}
			return [][]byte{payload}, nil
		}
		if item.Type == "function_call" {
			key := functionToolKey(item.Namespace, name)
			_, instrumented := t.commentaryTools[key]
			if instrumented {
				itemID, callID := item.ID, item.CallID
				if itemID == "" || callID == "" {
					return nil, staticCriticalDiagnostic("malformed_commentary_call", "the upstream emitted a malformed commentary function call")
				}
				if len(t.pending) >= maxMekugiPendingCalls {
					return nil, staticCriticalDiagnostic("commentary_call_capacity", "the upstream commentary call capacity was exceeded")
				}
				if _, exists := t.pending[itemID]; exists || t.pendingCallKnown(callID) {
					return nil, staticCriticalDiagnostic("reused_commentary_call", "the upstream reused a commentary call identity")
				}
				t.pending[itemID] = mekugiPendingCall{
					callID: callID, toolName: name, structured: true, added: bytes.Clone(payload),
				}
				return nil, nil
			}
		}
		return [][]byte{payload}, nil

	case envelope.Type == responseevents.CustomInputDelta:
		if fields := t.nativeExecCalls[envelope.ItemID]; fields != nil {
			t.previewStockDelta(envelope.ItemID, jsonString(fields, "name"), envelope.Delta)
		}
		return [][]byte{payload}, nil

	case envelope.Type == responseevents.FunctionArgumentsDelta:
		if pending, ok := t.pending[envelope.ItemID]; ok && pending.structured {
			return [][]byte{[]byte(`{"type":"response.in_progress"}`)}, nil
		}
		if fields := t.nativeExecCalls[envelope.ItemID]; fields != nil && jsonString(fields, "name") == nativeExecCommandToolName {
			t.previewStockDelta(envelope.ItemID, nativeExecCommandToolName, envelope.Delta)
		}
		return [][]byte{payload}, nil

	case envelope.Type == responseevents.CustomInputDone:
		t.endPreview(envelope.ItemID)
		if addedFields, stockCall := t.nativeExecCalls[envelope.ItemID]; stockCall {
			if jsonString(addedFields, "type") == "custom_tool_call" {
				addedCallID := jsonString(addedFields, "call_id")
				if addedCallID != "" && envelope.CallID != "" && addedCallID != envelope.CallID {
					return nil, staticCriticalDiagnostic("changed_code_mode_call", "the upstream changed a Code Mode call identity")
				}
				callID := cmp.Or(addedCallID, envelope.CallID)
				addedFields["call_id"] = mustMarshalJSON(callID)
				original := maps.Clone(addedFields)
				original["input"] = mustMarshalJSON(envelope.Input)
				item := newResponsesItem(original)
				changed, err := t.transformOutputItem(&item)
				if err != nil {
					return nil, err
				}
				event := payload
				if changed {
					event, err = replaceRawField(payload, "input", item.fields["input"])
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
		if fields := t.nativeExecCalls[envelope.ItemID]; fields != nil && jsonString(fields, "name") == nativeExecCommandToolName {
			t.endPreview(envelope.ItemID)
			return [][]byte{payload}, nil
		}
		pending, ok := t.pending[envelope.ItemID]
		if !ok {
			return [][]byte{payload}, nil
		}
		if len(pending.argumentsDone) != 0 {
			return nil, staticCriticalDiagnostic("repeated_commentary_arguments", "the upstream repeated commentary argument completion")
		}
		pending.argumentsDone = bytes.Clone(payload)
		t.pending[envelope.ItemID] = pending
		return [][]byte{[]byte(`{"type":"response.in_progress"}`)}, nil

	case envelope.Type == responseevents.OutputItemDone:
		item, ok := decodeResponsesItem(envelope.Item)
		if !ok {
			return [][]byte{payload}, nil //nolint:nilerr // Malformed unrelated output remains the upstream's responsibility.
		}
		t.endPreview(item.ID)
		activityFields := maps.Clone(item.fields)
		if _, delivered := t.local[item.CallID]; item.Status == "incomplete" && !delivered {
			// Item completion can report interrupted generation, not complete input.
			delete(t.pending, item.ID)
			delete(t.nativeExecCalls, item.ID)
			return [][]byte{payload}, nil
		}
		itemID := item.ID
		callID := item.CallID
		if addedFields, stockCall := t.nativeExecCalls[itemID]; stockCall {
			expectedCallID := jsonString(addedFields, "call_id")
			expectedType, expectedName := jsonString(addedFields, "type"), jsonString(addedFields, "name")
			if item.Type != expectedType || item.Name != expectedName || expectedCallID != "" && expectedCallID != callID {
				return nil, staticCriticalDiagnostic("inconsistent_stock_call", "the upstream completed an inconsistent stock tool call")
			}
		}
		delete(t.nativeExecCalls, itemID)
		if pending, buffered := t.pending[itemID]; buffered && pending.structured {
			if pending.callID != callID || len(pending.argumentsDone) == 0 {
				return nil, staticCriticalDiagnostic("inconsistent_commentary_call", "the upstream completed an inconsistent commentary function call")
			}
			message, err := t.transformStructuredCommentary(item.fields)
			if err != nil {
				return nil, err
			}
			var addedEnvelope struct {
				Item json.RawMessage `json:"item"`
			}
			var addedItem map[string]json.RawMessage
			if json.Unmarshal(pending.added, &addedEnvelope) != nil || json.Unmarshal(addedEnvelope.Item, &addedItem) != nil {
				return nil, staticCriticalDiagnostic("malformed_buffered_commentary_call", "Mekugi could not decode a buffered commentary call")
			}
			addedItem["arguments"] = item.fields["arguments"]
			addedPayload, err := marshalProtocolJSON(addedItem)
			if err != nil {
				return nil, err
			}
			addedEvent, err := replaceRawField(pending.added, "item", addedPayload)
			if err != nil {
				return nil, err
			}
			argumentsDone, err := replaceRawField(pending.argumentsDone, "arguments", item.fields["arguments"])
			if err != nil {
				return nil, err
			}
			itemPayload, err := marshalProtocolJSON(item)
			if err != nil {
				return nil, err
			}
			itemDone, err := replaceRawField(payload, "item", itemPayload)
			if err != nil {
				return nil, err
			}
			delete(t.pending, itemID)
			if err := t.commitLocalCall(callID); err != nil {
				return nil, err
			}
			t.collectSubagentToolCall(activityFields)
			if message != nil {
				return [][]byte{assistantCommentaryDoneEvent(message), addedEvent, argumentsDone, itemDone}, nil
			}
			return [][]byte{addedEvent, argumentsDone, itemDone}, nil
		}
		originalArguments := string(item.fields["arguments"])
		message, err := t.transformStructuredCommentary(item.fields)
		if err != nil {
			return nil, err
		}
		item = newResponsesItem(item.fields)
		changed, err := t.transformOutputItem(&item)
		if err != nil {
			return nil, err
		}
		if err := t.commitLocalCall(callID); err != nil {
			return nil, err
		}
		t.collectSubagentToolCall(activityFields)
		if !changed && message == nil && string(item.fields["arguments"]) == originalArguments {
			return [][]byte{payload}, nil
		}
		delete(t.pending, itemID)
		transformed, err := marshalProtocolJSON(item)
		if err != nil {
			return nil, err
		}
		event, err := replaceRawField(payload, "item", transformed)
		if err != nil {
			return nil, err
		}
		if message != nil {
			return [][]byte{assistantCommentaryDoneEvent(message), event}, nil
		}
		return [][]byte{event}, nil

	case envelope.Type.Terminal():
		clear(t.nativeExecCalls)
		if envelope.Type == responseevents.Completed {
			if len(t.pending) != 0 {
				return nil, staticCriticalDiagnostic("terminal_incomplete_intercepted_call", "the upstream completed with an incomplete intercepted function call")
			}
		} else {
			clear(t.pending)
		}
		transformed, usageMessage, err := t.transformResponse(envelope.Response, envelope.Type.Status())
		if err != nil {
			return nil, err
		}
		event, err := replaceRawField(payload, "response", transformed)
		if err != nil {
			return nil, err
		}
		var terminal struct {
			Status string `json:"status"`
		}
		if envelope.Type == responseevents.Completed && json.Unmarshal(transformed, &terminal) == nil && terminal.Status == "failed" {
			event, err = replaceRawField(event, "type", mustMarshalJSON(responseevents.Failed))
		}
		if err != nil {
			return nil, err
		}
		if err := t.Finish(true); err != nil {
			return nil, err
		}
		visible := make([][]byte, 0, len(t.commentarySubscriptions)+1)
		var threadMessages []map[string]json.RawMessage
		for _, subscription := range t.commentarySubscriptions {
			if !subscription.handedOff {
				continue
			}
			for _, publication := range t.proxy.commentary.drain(subscription.token) {
				if message := t.runtimeCommentaryMessage(publication); message != nil {
					if t.subagentTurn {
						threadMessages = append(threadMessages, message)
					} else {
						visible = append(visible, assistantCommentaryDoneEvent(message))
					}
				}
			}
		}
		for _, publication := range t.proxy.drainThreadCommentarySession(t.historySessionID, t.shellThreadID) {
			if message := t.runtimeCommentaryMessage(publication); message != nil {
				if t.subagentTurn {
					threadMessages = append(threadMessages, message)
				} else {
					visible = append(visible, assistantCommentaryDoneEvent(message))
				}
			}
		}
		if len(threadMessages) != 0 {
			var response map[string]json.RawMessage
			var output []map[string]json.RawMessage
			if err := json.Unmarshal(transformed, &response); err != nil {
				return nil, err
			}
			if raw, exists := response["output"]; exists {
				if err := json.Unmarshal(raw, &output); err != nil {
					return nil, err
				}
			}
			response["output"] = mustMarshalJSON(append(threadMessages, output...))
			event, err = replaceRawField(event, "response", mustMarshalJSON(response))
			if err != nil {
				return nil, err
			}
		}
		t.releaseCommentarySubscriptions()
		if usageMessage != nil {
			visible = append(visible, assistantCommentaryDoneEvent(usageMessage))
		}
		visible = append(visible, t.finalAnswer.flush()...)
		visible = append(visible, event)
		return visible, nil

	default:
		if _, pending := t.pending[envelope.ItemID]; pending || t.pendingCallKnown(envelope.CallID) {
			return nil, unsupportedMekugiStreamEvent(envelope.Type)
		}
		return [][]byte{payload}, nil
	}
}

func unsupportedMekugiStreamEvent(eventType responseevents.Kind) error {
	underlying := fmt.Errorf("unsupported intercepted stream event %q", eventType)
	switch {
	case eventType.FunctionArguments():
		return criticalDiagnostic(
			underlying,
			"unsupported_mekugi_stream_event:"+string(eventType),
			fmt.Sprintf("the upstream emitted unsupported intercepted streaming event %q", eventType),
			false,
		)
	default:
		// Unknown event names are provider-controlled payload. Their lexical
		// shape alone cannot establish that they are safe to display.
		return criticalDiagnostic(underlying, "unsupported_mekugi_stream_event", "the upstream emitted an unsupported intercepted streaming event", true)
	}
}

func (t *mekugiResponseTransform) pendingCallKnown(callID string) bool {
	for _, pending := range t.pending {
		if callID != "" && pending.callID == callID {
			return true
		}
	}
	return false
}

func (t *mekugiResponseTransform) transformResponse(payload []byte, terminalStatus string) ([]byte, map[string]json.RawMessage, error) {
	var counts tokenUsageReport
	observed := false
	// Discovery reads durable ancestry, so defer it until a report can actually
	// be emitted. Journal finish has its own terminal reporting path.
	if !t.subagentTurn && t.usageObserved && !t.journalTerminalReady() {
		substantive := t.finalAnswer.substantive && !t.finalAnswer.blocked && !t.finalAnswer.disabled
		if terminalStatus == "" {
			substantive = tokenUsageSubstantive(payload)
		}
		var identity struct {
			Status string `json:"status"`
		}
		if substantive && json.Unmarshal(payload, &identity) == nil && cmp.Or(terminalStatus, identity.Status) == "completed" {
			counts, observed = t.completionUsageReport()
		}
	}
	var object map[string]json.RawMessage
	var usageMessage map[string]json.RawMessage
	var err error
	if terminalStatus == "" {
		object, usageMessage, err = responseWithTokenUsageCommentary(payload, counts, observed && t.usageObserved, "")
	} else {
		err = json.Unmarshal(payload, &object)
		if err == nil && object == nil {
			err = errors.New("decode mekugi-enabled response")
		}
		// Codex consumes completed items, not the terminal output snapshot.
		usageMessage = formatTokenUsageCommentary(payload, counts, observed && t.usageObserved,
			terminalStatus, t.finalAnswer.substantive && !t.finalAnswer.blocked && !t.finalAnswer.disabled)
	}

	if err != nil {
		return nil, nil, err
	}
	if usageMessage != nil && len(t.retainCommentary(usageMessage)) == 0 {
		var output []map[string]json.RawMessage
		if terminalStatus == "" && json.Unmarshal(object["output"], &output) == nil {
			id := jsonString(usageMessage, "id")
			output = slices.DeleteFunc(output, func(item map[string]json.RawMessage) bool { return jsonString(item, "id") == id })
			object["output"] = mustMarshalJSON(output)
		}
		usageMessage = nil
	}
	if usageMessage != nil {
		t.liveDiffUsageID = jsonString(usageMessage, "id")
	}
	t.subagentResponses = t.retainCommentary(t.subagentResponses...)
	// SSE terminal events own completion even when the embedded status is absent.
	// JSON responses have no event envelope and retain body-status semantics.
	status := cmp.Or(terminalStatus, jsonString(object, "status"))
	interrupted := status == "failed" || status == "incomplete"
	var journalPrefix journalContinuation
	if t.journalActive {
		journalPrefix, _ = t.ctx.Value(journalContinuationKey{}).(journalContinuation)
	}
	if t.journalActive && terminalStatus != "" && (!interrupted || len(t.journalResults) != 0 || len(journalPrefix.clientOutput) != 0) {
		var output []map[string]json.RawMessage
		if err := decodeJournalOutput(object["output"], &output); err != nil {
			return nil, nil, errors.New("decode mekugi-enabled response output")
		}
		if len(output) == 0 {
			// A provider may leave the terminal snapshot empty after streaming
			// completed items. Rebuild it before adding journal results/notices:
			// a journal-only snapshot would replace WebSocket history and orphan
			// the next client tool result. Nonempty provider snapshots still own
			// their exact output, and the ordinary projection below restores carriers.
			object["output"] = mustMarshalJSON(t.journalProviderOutput)
		}
	}
	if rawOutput, ok := object["output"]; ok {
		var output []map[string]json.RawMessage
		if err := json.Unmarshal(rawOutput, &output); err != nil {
			return nil, nil, errors.New("decode mekugi-enabled response output")
		}
		if t.journalActive && terminalStatus == "" {
			t.journalProviderOutput = output
			for _, item := range output {
				if !isRouterLocalCall(item) && blocksTokenUsage(item) {
					t.journalClientCalls = true
				}
			}
		}
		activityMessages := t.retainCommentary(t.drainActivity()...)
		t.activityMessages = append(t.activityMessages, activityMessages...)
		transformedOutput := append([]map[string]json.RawMessage{}, t.activityMessages...)
		for _, publication := range t.deferredCommentary {
			if message := t.runtimeCommentaryMessage(publication); message != nil {
				transformedOutput = append(transformedOutput, message)
			}
		}
		t.deferredCommentary = nil
		transformedOutput = append(transformedOutput, t.subagentResponses...)
		t.subagentDeferred = nil
		for _, fields := range output {
			if t.journalActive && isRouterLocalCall(fields) {
				// A completed stream event can omit status. Its already-executed
				// result must survive a later interruption without running a new call.
				if !interrupted || jsonString(fields, "status") == "completed" || t.journalCalls[jsonString(fields, "call_id")] != nil {
					result, err := t.executeRouterLocalCall(fields)
					if err != nil {
						return nil, nil, err
					}
					transformedOutput = append(transformedOutput, journalClientResult(result, jsonString(fields, "name")))
				}
				continue
			}
			item := newResponsesItem(fields)
			// An interrupted response can contain partial calls. Only complete items
			// or calls whose complete input was already delivered may be projected.
			_, delivered := t.local[item.CallID]
			if !delivered && (interrupted && item.Status != "completed" || item.Status == "in_progress" || item.Status == "incomplete") {
				transformedOutput = append(transformedOutput, fields)
				continue
			}
			t.collectProviderCommentary(item.fields)
			activityFields := maps.Clone(item.fields)
			message, err := t.transformStructuredCommentary(item.fields)
			if err != nil {
				return nil, nil, err
			}
			if message != nil {
				transformedOutput = append(transformedOutput, message)
			}
			item = newResponsesItem(item.fields)
			if _, err := t.transformOutputItem(&item); err != nil {
				return nil, nil, err
			}
			t.collectSubagentToolCall(activityFields)
			transformedOutput = append(transformedOutput, item.fields)
		}
		encoded, err := marshalProtocolJSON(transformedOutput)
		if err != nil {
			return nil, nil, err
		}
		if t.journalActive {
			t.journalClientOutput = transformedOutput
		}
		object["output"] = encoded
	}
	if t.journalActive {
		t.journalContinue = status == "completed" && len(t.journalResults) != 0 && !t.journalClientCalls && !t.journalTerminalReady()
		if len(journalPrefix.clientOutput) != 0 {
			var output []map[string]json.RawMessage
			if err := json.Unmarshal(object["output"], &output); err != nil {
				return nil, nil, err
			}
			object["output"] = mustMarshalJSON(append(slices.Clone(journalPrefix.clientOutput), output...))
		}
	}
	t.restoreResponseContract(object)
	// Every translated carrier in a JSON body is about to become visible,
	// regardless of whether the provider supplied a terminal response status.
	if err := t.commitHistory(); err != nil {
		return nil, nil, err
	}
	transformed, err := marshalProtocolJSON(object)
	if err == nil && usageMessage != nil && !t.journalActive {
		// Codex forwards only the child's final answer, not its preceding usage.
		t.proxy.activity.collect(t.threadID, jsonString(usageMessage, "id"), "usage", formatTokenUsageReport(counts))
	}
	return transformed, usageMessage, err
}

func (t *mekugiResponseTransform) restoreResponseContract(object map[string]json.RawMessage) {
	if _, ok := object["tools"]; ok {
		if !t.originalToolsPresent {
			delete(object, "tools")
		} else {
			object["tools"] = bytes.Clone(t.originalTools)
		}
	}
	if _, ok := object["tool_choice"]; ok {
		if !t.originalToolChoicePresent {
			delete(object, "tool_choice")
		} else {
			object["tool_choice"] = bytes.Clone(t.originalToolChoice)
		}
	}
}

func (t *mekugiResponseTransform) transformOutputItem(item *responsesItem) (bool, error) {
	name := item.Name
	if t.codeModeToolName != "" && name == t.codeModeToolName &&
		item.Type == "custom_tool_call" {
		callID := item.CallID
		if callID == "" {
			return false, errors.New("code Mode call has no call ID")
		}
		var originalInput string
		if item.Input != nil {
			originalInput = *item.Input
		}
		if retained, exists := t.local[callID]; exists && retained.ToolName == name {
			if retained.Script != originalInput {
				return false, fmt.Errorf("code Mode call %q changed input", callID)
			}
			retained.UpstreamItem = item.cloneFields()
			t.local[callID] = retained
			item.setInput(retained.CarrierPayload)
			return retained.CarrierPayload != originalInput, nil
		}
		input, changed, err := t.lowerCodeModeCommentary(callID, originalInput)
		if err != nil {
			return false, err
		}
		patches := nativePatchesInCall(name, originalInput, t.directory)
		if !changed && len(patches) == 0 {
			return false, nil
		}
		// Retain the provider input and captured baseline before exposing any
		// executable call. The stock host remains the only edit executor.
		history := mekugiHistory{
			ToolName: name,
			Script:   originalInput, CarrierKind: codeModeCarrierCustom,
			CarrierName: name, CarrierPayload: input, UpstreamItem: item.cloneFields(),
			ReplayCarrier:   !changed,
			NativePatches:   patches,
			ExecutingThread: t.shellThreadID,
		}
		if !changed {
			history.CommentaryMessageIDs = []string{commentaryMessageID(callID)}
		}
		t.recordLocal(callID, &history)
		if changed {
			item.setInput(input)
		}
		return changed, nil
	}
	if !t.nativeTools || name != applyPatchToolName || item.Type != "custom_tool_call" {
		return false, nil
	}
	callID := item.CallID
	if callID == "" || item.Input == nil {
		return false, errors.New("upstream emitted malformed stock apply_patch call")
	}
	input := *item.Input
	if retained, exists := t.local[callID]; exists {
		if retained.ToolName != name || retained.Script != input {
			return false, fmt.Errorf("stock apply_patch call %q changed input", callID)
		}
		retained.UpstreamItem = item.cloneFields()
		t.local[callID] = retained
		return false, nil
	}
	patches := nativePatchesInCall(name, input, t.directory)
	if len(patches) == 0 {
		return false, nil
	}
	history := mekugiHistory{
		ToolName: name, Script: input,
		CarrierKind: codeModeCarrierCustom, CarrierName: name, CarrierPayload: input,
		ReplayCarrier: true, UpstreamItem: item.cloneFields(), NativePatches: patches,
		ExecutingThread: t.shellThreadID,
	}
	t.recordLocal(callID, &history)
	return false, nil
}

func replaceRawField(payload []byte, name string, value json.RawMessage) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return nil, errors.New("decode stream event")
	}
	object[name] = value
	return marshalProtocolJSON(object)
}
