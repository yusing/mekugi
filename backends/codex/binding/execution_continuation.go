package binding

import (
	jsonv1 "encoding/json"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	json "encoding/json/v2"
	"strconv"
	"strings"
)

// Source: apps/mekugi/internal/router/execution_continuation.go:15:294@874a6e86.
// RetainedExecutionItem supplies already-admitted historical wire facts. A nil
// Evidence preserves classification from original input without asserting a carrier.
type RetainedExecutionItem struct {
	Evidence     *RetainedExecution
	UpstreamItem map[string]jsonv1.RawMessage
}

func qualifiedExecutionToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "." + name
}

const minimumStatusWaitMS = 300000

func statusWaitFloor(kind string, maximumJSON []byte) int {
	if kind != "integer" && kind != "number" {
		return 0
	}
	floor := minimumStatusWaitMS
	var maximum float64
	if json.Unmarshal(maximumJSON, &maximum) == nil && maximum > 0 && maximum < float64(floor) {
		floor = int(maximum)
	}
	return floor
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
				visit(tool.Nested, qualifiedExecutionToolName(namespace, tool.Name()))
				continue
			}
			if namespace != "" && namespace != "functions" {
				continue
			}
			name := strings.TrimPrefix(tool.Name(), "functions.")
			path := qualifiedExecutionToolName(namespace, tool.Name())
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
					Type    string            `json:"type"`
					Maximum jsonv1.RawMessage `json:"maximum"`
				} `json:"properties"`
			}
			if json.Unmarshal(tool.RawField("parameters"), &schema, jsonv1.DefaultOptionsV1()) != nil {
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

func (t executionContinuationTools) forCell(id string) ExecutionContinuation {
	result := ExecutionContinuation{Handle: map[string]any{"cell_id": id}}
	if t.cellWait != "" {
		args := map[string]any{"cell_id": id}
		if t.cellWaitMS > 0 {
			args["yield_time_ms"] = t.cellWaitMS
		}
		result.NextCall = &ExecutionNextCall{Tool: t.cellWait, Input: args}
	} else {
		result.Reason = "The host wait tool is not exposed in this request. Do not restart the running call."
	}
	return result
}

func (t executionContinuationTools) forSession(id int64) ExecutionContinuation {
	result := ExecutionContinuation{Handle: map[string]any{"session_id": id}}
	args := map[string]any{"session_id": id, "chars": ""}
	if t.sessionWait != "" {
		if t.sessionWaitMS > 0 {
			args["yield_time_ms"] = t.sessionWaitMS
		}
		result.NextCall = &ExecutionNextCall{Tool: t.sessionWait, Input: args}
	} else if t.exec != "" && t.nestedSessionWait && id <= 1<<53-1 {
		if t.nestedSessionWaitMS > 0 {
			args["yield_time_ms"] = t.nestedSessionWaitMS
		}
		source := "text(await tools.write_stdin(" + string(responses.MustMarshalJSON(args)) + "));"
		result.NextCall = &ExecutionNextCall{Tool: t.exec, Input: source}
	} else {
		result.Reason = "The host session continuation tool is not exposed in this request. Do not restart the running call."
	}
	return result
}

// Project only after replay validation has established the original calls.
// Cell provenance is request-local transcript evidence, not a session registry.
func ProjectExecutionContinuations(request *codexresponses.Request, catalog *codexresponses.ToolCatalog, execName string, visible map[string]RetainedExecutionItem) {
	var items []map[string]jsonv1.RawMessage
	if json.Unmarshal(request.Fields["input"], &items, jsonv1.DefaultOptionsV1()) != nil {
		return
	}
	tools := executionTools(catalog, execName)
	calls := make(map[string]ExecutionCall)
	cells := make(map[string]ExecutionCall)
	notices := make(map[int]ExecutionContinuation)
	pending := make(map[string]int)
	changed := false
	for index, item := range items {
		callID := responses.StringField(item, "call_id")
		if callID == "" {
			continue
		}
		switch responses.StringField(item, "type") {
		case "custom_tool_call", "function_call":
			history := visible[callID]
			call := ClassifyExecutionCall(item, history.Evidence, execName)
			delete(pending, call.ResumeHandle)
			calls[callID] = call
		case "custom_tool_call_output", "function_call_output":
			call, known := calls[callID]
			delete(calls, callID)
			if !known {
				if history, found := visible[callID]; found {
					call = ClassifyExecutionCall(history.UpstreamItem, history.Evidence, execName)
					delete(pending, call.ResumeHandle)
					known = true
				}
			}
			if !known {
				continue
			}
			if call.CellID != "" {
				origin := cells[call.CellID]
				call.NativePayload = origin.NativePayload
			}
			texts := ExecutionOutputTexts(item["output"])

			if len(texts) == 0 {
				continue
			}
			var notice *ExecutionContinuation
			if call.CodeMode {
				status, cell, body := CodeModeExecutionHeader(texts[0])
				if status == "running" && (call.CellID == "" || call.CellID == cell) {
					cells[cell] = call
					value := tools.forCell(cell)
					notice = &value
				} else if status != "" && status != "running" {
					delete(cells, call.CellID)
					if call.NativePayload && status != "Script terminated" {
						// The first text may include output when the host
						// serializes its content as one string.
						texts[0] = body
						var session int64
						for _, payload := range texts {
							if id := NativeJSONSession(payload); id != 0 {
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
			} else if call.Native {
				if session := NativeExecutionSession(texts[0]); session != 0 {
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
		var parts []jsonv1.RawMessage
		if json.Unmarshal(item["output"], &parts, jsonv1.DefaultOptionsV1()) == nil {
			kept := parts[:0:0]
			for _, part := range parts {
				if ExecutionAnnotationMatches(part, notice.Handle) && !responses.SameJSONValue(part, annotation) {
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
			output, added, err := codexresponses.AppendOutputNotice(item["output"], text)
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

func executionHandleKey(notice ExecutionContinuation) string {
	if cell, ok := notice.Handle["cell_id"].(string); ok {
		return "cell:" + cell
	}
	return "session:" + strconv.FormatInt(notice.Handle["session_id"].(int64), 10)
}
