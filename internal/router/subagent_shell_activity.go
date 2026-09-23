package router

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Keep the excerpt to one line and 120 characters, including the ellipsis.
func toolActivityCommandExcerpt(script string) string {
	morePrograms := false
	if shellsyntax.IsBatch(script) {
		if programs, err := shellsyntax.Split(script); err == nil {
			script = programs[0]
			morePrograms = len(programs) > 1
		}
	}
	if parsed, err := shellsyntax.Parse(script); err == nil {
		script = parsed.Body
	}
	script = strings.TrimSpace(script)
	line, _, more := strings.Cut(script, "\n")
	more = more || morePrograms
	line = strings.TrimSpace(line)
	runes := []rune(line)
	if len(runes) > 120 || more && len(runes) >= 120 {
		line = string(runes[:119])
		more = true
	}
	if more {
		line = strings.TrimSpace(line) + "…"
	}
	return line
}

func toolActivityShellCall(item map[string]json.RawMessage, name string, requireResultMetadata bool) (string, map[string]json.RawMessage, string) {
	name = strings.TrimPrefix(name, "functions.")
	input := jsonString(item, "arguments")
	if input == "" {
		input = jsonString(item, "input")
	}
	if name == "exec" {
		if nested, ok := toolActivityUnwrapExec(input, requireResultMetadata); ok {
			return toolActivityShellCall(nested, jsonString(nested, "name"), requireResultMetadata)
		}
	}
	var args map[string]json.RawMessage
	_ = json.Unmarshal([]byte(input), &args)
	script := input
	if args != nil {
		script = jsonString(args, "cmd")
		if script == "" {
			script = jsonString(args, "command")
		}
		var argv []string
		if json.Unmarshal(args["command"], &argv) == nil && len(argv) > 0 {
			script = workerCommand(argv[0], argv[1:])
			if len(argv) == 3 && (argv[0] == "bash" || argv[0] == "sh") && (argv[1] == "-c" || argv[1] == "-lc") {
				script = argv[2]
			}
		}
	}
	return name, args, script
}

func (t *mekugiResponseTransform) shellActivityDisplay(item map[string]json.RawMessage, name string) (string, bool) {
	name, args, _ := toolActivityShellCall(item, name, false)
	label, excerpt := "", ""
	switch name {
	case "wait":
		cell := jsonString(args, "cell_id")
		if cell == "" {
			return "", false
		}
		label = "Still Running"
		var terminate bool
		_ = json.Unmarshal(args["terminate"], &terminate)
		if terminate {
			label = "Stop"
		}
		if operation := t.activityCellOperations[cell]; operation != "" {
			return label + "\n" + operation, true
		}
		return label + " · operation unavailable", true
	case "write_stdin":
		if jsonString(args, "chars") != "" {
			return "", false
		}
		label = "Still Running"
		excerpt = t.activityShellSessions[strings.TrimSpace(string(args["session_id"]))]
	default:
		return "", false
	}
	if excerpt == "" {
		return label, true
	}
	return label + "\n" + commentaryCode(excerpt), true
}

// Reconstruct from this request's visible call/result pairs, not the latest
// command or another thread's session ID. No process state is retained globally.
func (t *mekugiResponseTransform) prepareShellActivity(input json.RawMessage) {
	var items []map[string]json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return
	}
	type shellCall struct {
		excerpt  string
		codeMode bool
	}
	calls := make(map[string]shellCall)
	t.activityShellSessions = make(map[string]string)
	t.activityCellOperations = make(map[string]string)
	cellExcerpts := make(map[string]string)
	type cellCall struct {
		cell, operation, excerpt string
	}
	cellCalls := make(map[string]cellCall)
	for _, item := range items {
		kind, callID := jsonString(item, "type"), jsonString(item, "call_id")
		if callID == "" {
			continue
		}
		if kind == "function_call" || kind == "custom_tool_call" {
			qualifiedName := qualifiedToolName(jsonString(item, "namespace"), jsonString(item, "name"))
			if len(cellCalls) < 1024 {
				name := strings.TrimPrefix(qualifiedName, "functions.")
				switch {
				case name == "exec":
					operation := subagentToolPreview(item, qualifiedName, t.shellActivityDisplay)
					operation = strings.TrimPrefix(strings.TrimPrefix(operation, "Run\n"), "Run JavaScript\n")
					operation = strings.TrimPrefix(strings.TrimPrefix(operation, "Still Running\n"), "Running stored script\n")
					if operation == "Still Running" || operation == "Running stored script · command unavailable" {
						operation = ""
					}
					cellCalls[callID] = cellCall{operation: operation}
				case name == "wait":
					var args map[string]json.RawMessage
					_ = json.Unmarshal([]byte(jsonString(item, "arguments")), &args)
					cell := jsonString(args, "cell_id")
					if cell != "" {
						cellCalls[callID] = cellCall{cell: cell, operation: t.activityCellOperations[cell], excerpt: cellExcerpts[cell]}
					}
				}
			}
			if len(calls) >= 1024 {
				continue
			}
			name, args, script := toolActivityShellCall(item, qualifiedToolName(jsonString(item, "namespace"), jsonString(item, "name")), true)
			switch name {
			case "exec_command":
				calls[callID] = shellCall{toolActivityCommandExcerpt(script), strings.TrimPrefix(qualifiedName, "functions.") == "exec"}
			case "write_stdin":
				calls[callID] = shellCall{t.activityShellSessions[strings.TrimSpace(string(args["session_id"]))], strings.TrimPrefix(qualifiedName, "functions.") == "exec"}
			}
			if call, ok := cellCalls[callID]; ok && calls[callID].codeMode {
				call.excerpt = calls[callID].excerpt
				cellCalls[callID] = call
			}
		} else if kind == "function_call_output" || kind == "custom_tool_call_output" {
			if call, ok := cellCalls[callID]; ok {
				delete(cellCalls, callID)
				if texts := executionOutputTexts(item["output"]); len(texts) > 0 {
					status, cell, _ := codeModeExecutionHeader(texts[0])
					if status == "running" && (call.cell == "" || call.cell == cell) && len(t.activityCellOperations) < 1024 {
						t.activityCellOperations[cell] = call.operation
						if call.excerpt != "" {
							cellExcerpts[cell] = call.excerpt
						}
					} else if status != "" && status != "running" {
						delete(t.activityCellOperations, call.cell)
						delete(cellExcerpts, call.cell)
						if call.cell != "" && call.excerpt != "" && len(t.activityShellSessions) < 1024 {
							if session := toolActivityCodeModeSession(item["output"]); session != "" {
								t.activityShellSessions[session] = call.excerpt
							}
						}
					}
				}
			}
			call := calls[callID]
			delete(calls, callID)
			if call.excerpt != "" && len(t.activityShellSessions) < 1024 {
				session := ""
				if call.codeMode {
					session = toolActivityCodeModeSession(item["output"])
				} else {
					session = toolActivityOutputSession(item["output"])
				}
				if session != "" {
					t.activityShellSessions[session] = call.excerpt
				}
			}
		}
	}
}

// A transparent Code Mode call prints the stock result after the host's
// completed-script header. Only that verified result body can name a session;
// arbitrary program output and output-only projections cannot.
func toolActivityCodeModeSession(raw json.RawMessage) string {
	texts := executionOutputTexts(raw)
	if len(texts) == 0 {
		return toolActivityOutputSession(raw)
	}
	status, _, body := codeModeExecutionHeader(texts[0])
	if status == "" && !strings.HasPrefix(texts[0], "Script ") {
		return toolActivityOutputSession(raw)
	}
	if status != "Script completed" {
		return ""
	}
	texts[0] = body
	var session int64
	for _, payload := range texts {
		if id := nativeJSONSession(payload); id != 0 {
			if session != 0 {
				return ""
			}
			session = id
		}
	}
	if session == 0 {
		return ""
	}
	return strconv.FormatInt(session, 10)
}

func toolActivityOutputSession(raw json.RawMessage) string {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) == nil && value != nil {
		var id json.Number
		if json.Unmarshal(value["session_id"], &id) == nil && id != "" {
			return string(id)
		}
		return ""
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		for _, part := range parts {
			if session := toolActivityOutputSession(mustMarshalJSON(part.Text)); session != "" {
				return session
			}
		}
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	if strings.HasPrefix(strings.TrimSpace(text), "{") {
		return toolActivityOutputSession(json.RawMessage(text))
	}
	// Native exec metadata precedes command output. Never inspect output lines
	// for a session marker, since the program can print arbitrary text.
	for line := range strings.SplitSeq(text, "\n") {
		if line == "Output:" || line == "Final output:" {
			break
		}
		if id, ok := strings.CutPrefix(line, "Process running with session ID "); ok {
			var number json.Number
			if json.Unmarshal([]byte(id), &number) == nil && number != "" {
				return string(number)
			}
		}
	}
	return ""
}
