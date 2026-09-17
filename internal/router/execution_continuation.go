package router

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Continuations describe the next host call at an observed yield. They never
// resume a process, allocate an ID, or retain execution state.
type executionNextCall struct {
	Tool  string `json:"tool"`
	Input any    `json:"input"`
}

type executionContinuation struct {
	Recovery *hpatchRecovery    `json:"hpatch_recovery,omitempty"`
	Handle   map[string]any     `json:"handle"`
	NextCall *executionNextCall `json:"next_call"`
	Reason   string             `json:"reason,omitempty"`
}

type executionContinuationTools struct {
	exec, cellWait, sessionWait string
	nestedSessionWait           bool
	cellWaitMS, sessionWaitMS   int
	nestedSessionWaitMS         int
}

func executionTools(catalog *responsesToolCatalog, execName string) executionContinuationTools {
	var found executionContinuationTools
	var visit func(*responsesToolSection, string)
	visit = func(section *responsesToolSection, namespace string) {
		if section == nil || section.err != nil {
			return
		}
		for _, tool := range section.tools {
			if tool == nil || tool.fields == nil {
				continue
			}
			if tool.Type == "namespace" {
				visit(tool.nested, qualifiedToolName(namespace, tool.Name))
				continue
			}
			if namespace != "" && namespace != "functions" {
				continue
			}
			name := strings.TrimPrefix(tool.Name, "functions.")
			path := qualifiedToolName(namespace, tool.Name)
			if tool.Type == "custom" && name == strings.TrimPrefix(execName, "functions.") {
				found.exec = path
				found.nestedSessionWait = hasCodeModeSessionWait(tool.Description)
				if section := codeModeSessionWaitSection(tool.Description); found.nestedSessionWait && strings.Contains(section, "yield_time_ms") {
					found.nestedSessionWaitMS = minimumStatusWaitMS
				}
			}
			if tool.Type != "function" {
				continue
			}
			var schema struct {
				Properties map[string]struct {
					Type    string          `json:"type"`
					Maximum json.RawMessage `json:"maximum"`
				} `json:"properties"`
			}
			if json.Unmarshal(tool.rawField("parameters"), &schema) != nil {
				continue
			}
			timing := schema.Properties["yield_time_ms"]
			waitMS := statusWaitFloor(timing.Type, timing.Maximum)
			switch name {
			case "wait":
				if schema.Properties["cell_id"].Type == "string" {
					found.cellWait = path
					found.cellWaitMS = waitMS
				}
			case "write_stdin":
				idType := schema.Properties["session_id"].Type
				if (idType == "number" || idType == "integer") && schema.Properties["chars"].Type == "string" {
					found.sessionWait = path
					found.sessionWaitMS = waitMS
				}
			}
		}
	}
	visit(catalog.top, "")
	for _, group := range catalog.additional {
		visit(group.tools, "")
	}
	return found
}

func hasCodeModeSessionWait(description string) bool {
	section := codeModeSessionWaitSection(description)
	return strings.Contains(section, "declare const tools: { write_stdin(") &&
		strings.Contains(section, "session_id") && strings.Contains(section, "chars")
}

func codeModeSessionWaitSection(description string) string {
	for _, heading := range []string{"### `write_stdin`", "### write_stdin"} {
		start := strings.Index(description, heading)
		if start < 0 || start > 0 && description[start-1] != '\n' {
			continue
		}
		section := description[start+len(heading):]
		for _, next := range []string{"\n### ", "\n## "} {
			if end := strings.Index(section, next); end >= 0 {
				section = section[:end]
			}
		}
		return section
	}
	return ""
}

func (t executionContinuationTools) forCell(id string) executionContinuation {
	result := executionContinuation{Handle: map[string]any{"cell_id": id}}
	if t.cellWait != "" {
		args := map[string]any{"cell_id": id}
		if t.cellWaitMS > 0 {
			args["yield_time_ms"] = t.cellWaitMS
		}
		result.NextCall = &executionNextCall{Tool: t.cellWait, Input: args}
	} else {
		result.Reason = "The host wait tool is not exposed in this request. Do not restart the running call."
	}
	return result
}

func (t executionContinuationTools) forSession(id int64) executionContinuation {
	result := executionContinuation{Handle: map[string]any{"session_id": id}}
	args := map[string]any{"session_id": id, "chars": ""}
	if t.sessionWait != "" {
		if t.sessionWaitMS > 0 {
			args["yield_time_ms"] = t.sessionWaitMS
		}
		result.NextCall = &executionNextCall{Tool: t.sessionWait, Input: args}
	} else if t.exec != "" && t.nestedSessionWait && id <= 1<<53-1 {
		if t.nestedSessionWaitMS > 0 {
			args["yield_time_ms"] = t.nestedSessionWaitMS
		}
		source := "text(await tools.write_stdin(" + string(mustMarshalJSON(args)) + "));"
		if t.nestedSessionWaitMS > 0 {
			// Keep the enclosing cell from yielding at its shorter default while
			// the native wait is still pending. The host clamps its own limit.
			source = "// @exec: {\"yield_time_ms\":300000}\n" + source
		}
		result.NextCall = &executionNextCall{Tool: t.exec, Input: source}
	} else {
		result.Reason = "The host session continuation tool is not exposed in this request. Do not restart the running call."
	}
	return result
}

type mixedOutputProjection struct {
	recovery func(hpatchRecovery) hpatchRecovery
	success  func(string, json.RawMessage, hpatchRecovery) (json.RawMessage, bool)
}

type executionCall struct {
	codeMode      bool
	native        bool
	nativePayload bool
	cellID        string
	resumeHandle  string
	mixed         *hpatchRecovery
	mixedCallID   string
}

func executionResumeHandle(item map[string]json.RawMessage, history mekugiHistory, known bool, execName string) string {
	if namespace := jsonString(item, "namespace"); namespace != "" && namespace != "functions" {
		return ""
	}
	name := strings.TrimPrefix(jsonString(item, "name"), "functions.")
	if name == strings.TrimPrefix(execName, "functions.") {
		source := jsonString(item, "input")
		if known && history.TranslationError == "" && history.ToolName == codeModeCommentaryHistoryTool {
			source = history.Script
		}
		nested, ok := toolActivityUnwrapExec(source, false)
		if !ok {
			return ""
		}
		item = nested
		name = jsonString(item, "name")
	}
	var args struct {
		CellID    string `json:"cell_id"`
		SessionID int64  `json:"session_id"`
	}
	if json.Unmarshal([]byte(jsonString(item, "arguments")), &args) != nil {
		return ""
	}
	if name == "wait" && args.CellID != "" {
		return "cell:" + args.CellID
	}
	if name == "write_stdin" && args.SessionID > 0 {
		return "session:" + strconv.FormatInt(args.SessionID, 10)
	}
	return ""
}

// Match only the host's leading metadata, never strings printed by the program
// below Output:. Native session metadata inside Code Mode needs a verified
// router carrier or transparent native-result projection as well.
func codeModeExecutionHeader(text string) (status, cell string, body string) {
	first, rest, ok := strings.Cut(text, "\n")
	if !ok {
		return "", "", ""
	}
	switch {
	case strings.HasPrefix(first, "Script running with cell ID "):
		cell = strings.TrimPrefix(first, "Script running with cell ID ")
		if cell == "" || strings.ContainsAny(cell, " \t\r\n") {
			return "", "", ""
		}
		status = "running"
	case first == "Script completed" || first == "Script failed" || first == "Script terminated":
		status = first
	default:
		return "", "", ""
	}
	wall, rest, ok := strings.Cut(rest, "\n")
	if !ok || !strings.HasPrefix(wall, "Wall time ") || !strings.HasSuffix(wall, " seconds") {
		return "", "", ""
	}
	duration := strings.TrimSuffix(strings.TrimPrefix(wall, "Wall time "), " seconds")
	if _, err := strconv.ParseFloat(duration, 64); err != nil {
		return "", "", ""
	}
	body, ok = strings.CutPrefix(rest, "Output:\n")
	if !ok {
		return "", "", ""
	}
	return status, cell, body
}

func nativeExecutionHeader(text string) (state, body string) {
	line, rest, ok := strings.Cut(text, "\n")
	if strings.HasPrefix(line, "Chunk ID: ") {
		line, rest, ok = strings.Cut(rest, "\n")
	}
	if !ok || !strings.HasPrefix(line, "Wall time: ") || !strings.HasSuffix(line, " seconds") {
		return "", ""
	}
	duration := strings.TrimSuffix(strings.TrimPrefix(line, "Wall time: "), " seconds")
	if _, err := strconv.ParseFloat(duration, 64); err != nil {
		return "", ""
	}
	state, rest, ok = strings.Cut(rest, "\n")
	if !ok {
		return "", ""
	}
	line, rest, ok = strings.Cut(rest, "\n")
	if after, ok0 := strings.CutPrefix(line, "Original token count: "); ok0 {
		if _, err := strconv.ParseUint(after, 10, 64); err != nil {
			return "", ""
		}
		line, rest, ok = strings.Cut(rest, "\n")
	}
	if !ok || (line != "Output:" && line != "Final output:") {
		return "", ""
	}
	return state, rest
}

func nativeExecutionSession(text string) int64 {
	state, _ := nativeExecutionHeader(text)
	id, running := strings.CutPrefix(state, "Process running with session ID ")
	if !running {
		return 0
	}
	value, err := strconv.ParseInt(id, 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func nativeJSONSession(text string) int64 {
	var result map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &result) != nil {
		return 0
	}
	if batch, exists := result["results"]; exists {
		var results []map[string]json.RawMessage
		if json.Unmarshal(batch, &results) != nil || len(results) == 0 {
			return 0
		}
		// Completed prefixes have no live session. Only the current final
		// program can yield when a sequential carrier exits on a host error.
		result = results[len(results)-1]
	}
	var output string
	var id int64
	if json.Unmarshal(result["output"], &output) != nil ||
		json.Unmarshal(result["session_id"], &id) != nil || id <= 0 {
		return 0
	}
	if exit, exists := result["exit_code"]; exists && string(exit) != "null" {
		return 0
	}
	return id
}

func executionOutputTexts(raw json.RawMessage) []string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []string{text}
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 || parts[0].Type != "input_text" {
		return nil
	}
	texts := make([]string, len(parts))
	for i, part := range parts {
		if part.Type == "input_text" {
			texts[i] = part.Text
		}
	}
	return texts
}

func executionCallFor(item map[string]json.RawMessage, history mekugiHistory, known bool, execName string) executionCall {
	if namespace := jsonString(item, "namespace"); namespace != "" && namespace != "functions" {
		return executionCall{}
	}
	name := strings.TrimPrefix(jsonString(item, "name"), "functions.")
	call := executionCall{resumeHandle: executionResumeHandle(item, history, known, execName)}
	if after, ok := strings.CutPrefix(call.resumeHandle, "cell:"); ok {
		call.cellID = after
	}
	if known {
		call.mixed = hpatchRecoveryFor(history)
		if call.mixed != nil {
			call.resumeHandle = "hpatch:" + call.mixed.Handle
		}
	}
	if known && history.TranslationError == "" {
		call.codeMode = history.effectiveCarrierKind() == codeModeCarrierCustom
		call.native = history.effectiveCarrierKind() == codeModeCarrierFunction
		call.nativePayload = history.PluginID == builtinToolsPluginID && history.ToolName == "shell" && !history.ReplayCarrier
		if call.codeMode && history.PluginID == "" && history.ToolName == codeModeCommentaryHistoryTool {
			if nested, ok := toolActivityUnwrapExec(history.Script, true); ok {
				call.nativePayload = jsonString(nested, "name") == "exec_command" || jsonString(nested, "name") == "write_stdin"
			}
		}
		if call.codeMode || call.native {
			return call
		}
	}
	switch name {
	case "exec_command", "write_stdin":
		call.native = true
	case strings.TrimPrefix(execName, "functions."):
		call.codeMode = true
		if nested, ok := toolActivityUnwrapExec(jsonString(item, "input"), true); ok {
			call.nativePayload = jsonString(nested, "name") == "exec_command" || jsonString(nested, "name") == "write_stdin"
		}
	case "wait":
		var args struct {
			CellID string `json:"cell_id"`
		}
		if json.Unmarshal([]byte(jsonString(item, "arguments")), &args) == nil {
			call.cellID = args.CellID
			call.codeMode = args.CellID != ""
		}
	}
	return call
}

// Project only after replay validation has established the original calls.
// Cell provenance is request-local transcript evidence, not a session registry.
func projectExecutionContinuations(mixed *mixedOutputProjection, request *parsedResponsesRequest, catalog *responsesToolCatalog, execName string, visible map[string]mekugiHistory) {
	var items []map[string]json.RawMessage
	if json.Unmarshal(request.fields["input"], &items) != nil {
		return
	}
	tools := executionTools(catalog, execName)
	calls := make(map[string]executionCall)
	cells := make(map[string]executionCall)
	notices := make(map[int]executionContinuation)
	pending := make(map[string]int)
	changed := false
	for index, item := range items {
		callID := jsonString(item, "call_id")
		if callID == "" {
			continue
		}
		switch jsonString(item, "type") {
		case "custom_tool_call", "function_call":
			history, known := visible[callID]
			call := executionCallFor(item, history, known, execName)
			delete(pending, call.resumeHandle)
			calls[callID] = call
		case "custom_tool_call_output", "function_call_output":
			call, known := calls[callID]
			delete(calls, callID)
			if !known {
				if history, found := visible[callID]; found {
					call = executionCallFor(history.UpstreamItem, history, true, execName)
					delete(pending, call.resumeHandle)
					known = true
				}
			}
			if !known {
				continue
			}
			if call.cellID != "" {
				origin := cells[call.cellID]
				call.nativePayload, call.mixed, call.mixedCallID = origin.nativePayload, origin.mixed, origin.mixedCallID
			}
			if call.mixed != nil && call.mixedCallID == "" {
				call.mixedCallID = callID
			}
			texts := executionOutputTexts(item["output"])
			mixedComplete := false
			if call.mixed != nil && mixed != nil && mixed.success != nil {
				var output json.RawMessage
				output, mixedComplete = mixed.success(call.mixedCallID, item["output"], *call.mixed)
				if mixedComplete && !sameJSONValue(output, item["output"]) {
					item["output"] = output
					changed = true
				}
			}

			if len(texts) == 0 {
				continue
			}
			var notice *executionContinuation
			if call.codeMode {
				status, cell, body := codeModeExecutionHeader(texts[0])
				if status == "running" && (call.cellID == "" || call.cellID == cell) {
					cells[cell] = call
					value := tools.forCell(cell)
					notice = &value
				} else if status != "" && status != "running" {
					delete(cells, call.cellID)
					if call.nativePayload && status != "Script terminated" {
						// The first text may include output when the host
						// serializes its content as one string.
						texts[0] = body
						var session int64
						for _, payload := range texts {
							if id := nativeJSONSession(payload); id != 0 {
								if session != 0 {
									session = 0
									break
								}
								session = id
							}
						}
						if session != 0 {
							value := tools.forSession(session)
							notice = &value
						}
					}
				}
			} else if call.native {
				if session := nativeExecutionSession(texts[0]); session != 0 {
					value := tools.forSession(session)
					notice = &value
				}
			}
			if call.mixed != nil && !mixedComplete && !hasHpatchFinalResult(texts, call.mixed.Handle) {
				if notice == nil {
					notice = &executionContinuation{
						Handle: map[string]any{"hpatch": call.mixed.Handle},
						Reason: "No complete mixed-script result was returned. Resolve the previous cell and potentially live sessions before using the resume handle; missing confirmation does not mean rollback.",
					}
				}
				notice.Recovery = call.mixed
			}
			if notice == nil {
				continue
			}
			notices[index] = *notice
			pending[executionHandleKey(*notice)] = index
		}
	}
	// A continuation call consumes the earlier suggestion, even while its result
	// is pending. Only a later yield can advertise that handle again.
	for index, notice := range notices {
		item := items[index]
		latest, outstanding := pending[executionHandleKey(notice)]
		outstanding = outstanding && latest == index
		if outstanding && notice.Recovery != nil && mixed != nil && mixed.recovery != nil {
			recovery := mixed.recovery(*notice.Recovery)
			notice.Recovery = &recovery
		}
		text := string(mustMarshalJSON(map[string]any{"continuation": notice}))
		annotation := mustMarshalJSON(map[string]string{"type": "input_text", "text": text})
		// Re-prepared requests can contain annotations from an older catalog.
		// Retire those too, while preserving host output and unrelated warnings.
		var parts []json.RawMessage
		if json.Unmarshal(item["output"], &parts) == nil {
			kept := parts[:0:0]
			for _, part := range parts {
				if executionAnnotationMatches(part, notice) && (!outstanding || !sameJSONValue(part, annotation)) {
					changed = true
					continue
				}
				kept = append(kept, part)
			}
			if len(kept) != len(parts) {
				item["output"] = mustMarshalJSON(kept)
			}
		}
		if outstanding {
			output, added, err := appendToolOutputWarning(item["output"], text)
			if err == nil && added {
				item["output"] = output
				changed = true
			}
		}
	}
	if changed {
		request.setInput(mustMarshalJSON(items))
	}
}

func executionAnnotationMatches(part json.RawMessage, notice executionContinuation) bool {
	var content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(part, &content) != nil || content.Type != "input_text" || !sameJSONValue(part, mustMarshalJSON(content)) {
		return false
	}
	var envelope struct {
		Continuation *executionContinuation `json:"continuation"`
	}
	if json.Unmarshal([]byte(content.Text), &envelope) != nil || envelope.Continuation == nil {
		return false
	}
	return sameJSONValue([]byte(content.Text), mustMarshalJSON(envelope)) &&
		sameJSONValue(mustMarshalJSON(envelope.Continuation.Handle), mustMarshalJSON(notice.Handle))
}

func executionHandleKey(notice executionContinuation) string {
	if handle, ok := notice.Handle["hpatch"].(string); ok {
		return "hpatch:" + handle
	}
	if cell, ok := notice.Handle["cell_id"].(string); ok {
		return "cell:" + cell
	}
	return "session:" + strconv.FormatInt(notice.Handle["session_id"].(int64), 10)
}
