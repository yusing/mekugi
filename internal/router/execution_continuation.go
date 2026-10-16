package router

import (
	"encoding/json"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv2 "encoding/json/v2"
	"strconv"
	"strings"
)

const minimumStatusWaitMS = 300000

func statusWaitFloor(kind string, maximumJSON []byte) int {
	if kind != "integer" && kind != "number" {
		return 0
	}
	floor := minimumStatusWaitMS
	var maximum float64
	if jsonv2.Unmarshal(maximumJSON, &maximum) == nil && maximum > 0 && maximum < float64(floor) {
		floor = int(maximum)
	}
	return floor
}

// Continuations describe the next host call at an observed yield. They never
// resume a process, allocate an ID, or retain execution state.
type executionNextCall struct {
	Tool  string `json:"tool"`
	Input any    `json:"input"`
}

type executionContinuation struct {
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

func executionTools(catalog *codexresponses.ToolCatalog, execName string) executionContinuationTools {
	var found executionContinuationTools
	var visit func(*codexresponses.ToolSection, string)
	visit = func(section *codexresponses.ToolSection, namespace string) {
		if section == nil || section.Err != nil {
			return
		}
		for _, tool := range section.Tools {
			if !tool.IsObject() {
				continue
			}
			if tool.Type() == "namespace" {
				visit(tool.Nested, qualifiedToolName(namespace, tool.Name()))
				continue
			}
			if namespace != "" && namespace != "functions" {
				continue
			}
			name := strings.TrimPrefix(tool.Name(), "functions.")
			path := qualifiedToolName(namespace, tool.Name())
			if tool.Type() == "custom" && name == strings.TrimPrefix(execName, "functions.") {
				found.exec = path
				waitSection := codeModeSessionWaitSection(tool.Description())
				found.nestedSessionWait = strings.Contains(waitSection, "declare const tools: { write_stdin(") &&
					strings.Contains(waitSection, "session_id") && strings.Contains(waitSection, "chars")
				if found.nestedSessionWait && strings.Contains(waitSection, "yield_time_ms") {
					found.nestedSessionWaitMS = minimumStatusWaitMS
				}
			}
			if tool.Type() != "function" {
				continue
			}
			var schema struct {
				Properties map[string]struct {
					Type    string          `json:"type"`
					Maximum json.RawMessage `json:"maximum"`
				} `json:"properties"`
			}
			if json.Unmarshal(tool.RawField("parameters"), &schema) != nil {
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
	visit(catalog.Top, "")
	for _, group := range catalog.Additional {
		visit(group.Tools, "")
	}
	return found
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
		source := "text(await tools.write_stdin(" + string(responses.MustMarshalJSON(args)) + "));"
		result.NextCall = &executionNextCall{Tool: t.exec, Input: source}
	} else {
		result.Reason = "The host session continuation tool is not exposed in this request. Do not restart the running call."
	}
	return result
}

type executionCall struct {
	codeMode      bool
	native        bool
	nativePayload bool
	cellID        string
	resumeHandle  string
}

func executionResumeHandle(item map[string]json.RawMessage, history mekugiHistory, known bool, execName string) string {
	if namespace := responses.StringField(item, "namespace"); namespace != "" && namespace != "functions" {
		return ""
	}
	name := strings.TrimPrefix(responses.StringField(item, "name"), "functions.")
	if name == strings.TrimPrefix(execName, "functions.") {
		source := responses.StringField(item, "input")
		nested, ok := binding.SingleExecutionCall(source, false)
		if !ok {
			return ""
		}
		item = nested
		name = responses.StringField(item, "name")
	}
	var args struct {
		CellID    string `json:"cell_id"`
		SessionID int64  `json:"session_id"`
	}
	if json.Unmarshal([]byte(responses.StringField(item, "arguments")), &args) != nil {
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
	if namespace := responses.StringField(item, "namespace"); namespace != "" && namespace != "functions" {
		return executionCall{}
	}
	name := strings.TrimPrefix(responses.StringField(item, "name"), "functions.")
	call := executionCall{resumeHandle: executionResumeHandle(item, history, known, execName)}
	if after, ok := strings.CutPrefix(call.resumeHandle, "cell:"); ok {
		call.cellID = after
	}
	if known && history.TranslationError == "" {
		call.codeMode = history.effectiveCarrierKind() == codeModeCarrierCustom
		call.native = history.effectiveCarrierKind() == codeModeCarrierFunction
		call.nativePayload = history.ToolName == nativeExecCommandToolName
		if call.codeMode && history.ToolName == "exec" {
			if nested, ok := binding.SingleExecutionCall(history.Script, true); ok {
				name := responses.StringField(nested, "name")
				call.nativePayload = name == nativeExecCommandToolName || name == "write_stdin"
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
		if nested, ok := binding.SingleExecutionCall(responses.StringField(item, "input"), true); ok {
			call.nativePayload = responses.StringField(nested, "name") == "exec_command" || responses.StringField(nested, "name") == "write_stdin"
		}
	case "wait":
		var args struct {
			CellID string `json:"cell_id"`
		}
		if json.Unmarshal([]byte(responses.StringField(item, "arguments")), &args) == nil {
			call.cellID = args.CellID
			call.codeMode = args.CellID != ""
		}
	}
	return call
}

// Project only after replay validation has established the original calls.
// Cell provenance is request-local transcript evidence, not a session registry.
func projectExecutionContinuations(request *codexresponses.Request, catalog *codexresponses.ToolCatalog, execName string, visible map[string]mekugiHistory) {
	var items []map[string]json.RawMessage
	if json.Unmarshal(request.Fields["input"], &items) != nil {
		return
	}
	tools := executionTools(catalog, execName)
	calls := make(map[string]executionCall)
	cells := make(map[string]executionCall)
	notices := make(map[int]executionContinuation)
	pending := make(map[string]int)
	changed := false
	for index, item := range items {
		callID := responses.StringField(item, "call_id")
		if callID == "" {
			continue
		}
		switch responses.StringField(item, "type") {
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
				call.nativePayload = origin.nativePayload
			}
			texts := executionOutputTexts(item["output"])

			if len(texts) == 0 {
				continue
			}
			var notice *executionContinuation
			if call.codeMode {
				status, cell, body := binding.CodeModeExecutionHeader(texts[0])
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
							if id := binding.NativeJSONSession(payload); id != 0 {
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
				if session := binding.NativeExecutionSession(texts[0]); session != 0 {
					value := tools.forSession(session)
					notice = &value
				}
			}
			if notice == nil {
				continue
			}
			notices[index] = *notice
			pending[executionHandleKey(*notice)] = index
		}
	}
	// Reconstruct available yielded suggestions, including historical ones. Native
	// WebSocket history does not retain projections, but the provider does. A
	// later wait result makes earlier advice historical without removing it from
	// the confirmed prefix. Unavailable advice is kept only while outstanding;
	// a tool-catalog change may replace obsolete advice.
	for index, notice := range notices {
		item := items[index]
		latest, outstanding := pending[executionHandleKey(notice)]
		outstanding = outstanding && latest == index
		text := string(responses.MustMarshalJSON(map[string]any{"continuation": notice}))
		annotation := responses.MustMarshalJSON(map[string]string{"type": "input_text", "text": text})
		// Re-prepared requests can contain annotations from an older catalog.
		// Replace only obsolete advice, preserving host output and already-sent
		// annotations whose tool contract has not changed.
		var parts []json.RawMessage
		if json.Unmarshal(item["output"], &parts) == nil {
			kept := parts[:0:0]
			for _, part := range parts {
				if executionAnnotationMatches(part, notice) && !responses.SameJSONValue(part, annotation) {
					changed = true
					continue
				}
				kept = append(kept, part)
			}
			if len(kept) != len(parts) {
				item["output"] = responses.MustMarshalJSON(kept)
			}
		}
		if outstanding || notice.NextCall != nil {
			output, added, err := appendToolOutputWarning(item["output"], text)
			if err == nil && added {
				item["output"] = output
				changed = true
			}
		}
	}
	if changed {
		request.Fields["input"] = responses.MustMarshalJSON(items)
	}
}

func executionAnnotationMatches(part json.RawMessage, notice executionContinuation) bool {
	var content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(part, &content) != nil || content.Type != "input_text" || !responses.SameJSONValue(part, responses.MustMarshalJSON(content)) {
		return false
	}
	var envelope struct {
		Continuation *executionContinuation `json:"continuation"`
	}
	if json.Unmarshal([]byte(content.Text), &envelope) != nil || envelope.Continuation == nil {
		return false
	}
	return responses.SameJSONValue([]byte(content.Text), responses.MustMarshalJSON(envelope)) &&
		responses.SameJSONValue(responses.MustMarshalJSON(envelope.Continuation.Handle), responses.MustMarshalJSON(notice.Handle))
}

func executionHandleKey(notice executionContinuation) string {
	if cell, ok := notice.Handle["cell_id"].(string); ok {
		return "cell:" + cell
	}
	return "session:" + strconv.FormatInt(notice.Handle["session_id"].(int64), 10)
}
