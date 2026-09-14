package router

import (
	"encoding/json"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Only presentation reads these references. In particular, accepting a params
// header here does not make that combination valid executable shell syntax.
func toolActivityScriptReference(script string) string {
	script = strings.TrimSpace(script)
	if strings.HasPrefix(script, "#!params=") {
		_, script, _ = strings.Cut(script, "\n")
	}
	parsed, err := shellsyntax.Parse(strings.TrimSpace(script))
	if err == nil && parsed.HasScript {
		return "#!script=" + parsed.ScriptPath
	}
	return ""
}

// Keep the excerpt to one line and 120 characters, including the ellipsis.
func toolActivityCommandExcerpt(script string) string {
	script, _ = toolActivityUnwrapShell(script, "bash")
	morePrograms := false
	if _, _, batch := shellsyntax.BatchHeader(script); batch {
		if programs, err := shellsyntax.Split(script); err == nil {
			script = programs[0]
			morePrograms = len(programs) > 1
		}
	}
	if parsed, err := shellsyntax.Parse(script); err == nil && !parsed.HasScript {
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
	if name == "shell" || name == "exec_command" || name == "shell_command" {
		script, _ = toolActivityUnwrapShell(script, "bash")
	}
	return name, args, script
}

func (t *mekugiResponseTransform) shellActivityExcerpt(script string) string {
	if reference := toolActivityScriptReference(script); reference != "" {
		resolved, err := t.proxy.resolveShellInput(t.shellDirectory, reference)
		if err != nil {
			return ""
		}
		script = resolved
	}
	return toolActivityCommandExcerpt(script)
}

func (t *mekugiResponseTransform) shellActivityDisplay(item map[string]json.RawMessage, name string) (string, bool) {
	name, args, script := toolActivityShellCall(item, name, false)
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
	case "shell", "shell_command", "exec_command":
		if toolActivityScriptReference(script) == "" {
			return "", false
		}
		label = "Running stored script"
		excerpt = t.shellActivityExcerpt(script)
	default:
		return "", false
	}
	if excerpt == "" {
		return label + " · command unavailable", true
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
	calls := make(map[string]string)
	t.activityShellSessions = make(map[string]string)
	t.activityCellOperations = make(map[string]string)
	type cellCall struct {
		cell, operation string
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
				history, known := t.visible[callID]
				name := strings.TrimPrefix(qualifiedName, "functions.")
				// Replay restores the public tool identity (for example shell),
				// but its recorded carrier still owns the host's cell metadata.
				switch {
				case name == "exec" || known && history.TranslationError == "" && history.effectiveCarrierKind() == codeModeCarrierCustom:
					operation := subagentToolPreview(item, qualifiedName, t.shellActivityDisplay)
					if history.ToolName == "shell" && history.PluginID == builtinToolsPluginID &&
						!history.ReplayCarrier && jsonString(history.UpstreamItem, "name") == t.codeModeToolName {
						operation = toolActivityShell(history.Script)
					}
					operation = strings.TrimPrefix(strings.TrimPrefix(operation, "Run\n"), "Run JavaScript\n")
					operation = strings.TrimPrefix(strings.TrimPrefix(operation, "Still Running\n"), "Running stored script\n")
					if operation == "Still Running · command unavailable" || operation == "Running stored script · command unavailable" {
						operation = ""
					}
					cellCalls[callID] = cellCall{operation: operation}
				case name == "wait":
					var args map[string]json.RawMessage
					_ = json.Unmarshal([]byte(jsonString(item, "arguments")), &args)
					cell := jsonString(args, "cell_id")
					if cell != "" {
						cellCalls[callID] = cellCall{cell: cell, operation: t.activityCellOperations[cell]}
					}
				}
			}
			if len(calls) >= 1024 {
				continue
			}
			name, args, script := toolActivityShellCall(item, qualifiedToolName(jsonString(item, "namespace"), jsonString(item, "name")), true)
			if history, ok := t.visible[callID]; ok &&
				history.ToolName == "shell" && history.PluginID == builtinToolsPluginID &&
				!history.ReplayCarrier && jsonString(history.UpstreamItem, "name") == t.codeModeToolName {
				name, script = "shell", history.Script
			}
			switch name {
			case "shell", "shell_command", "exec_command":
				calls[callID] = t.shellActivityExcerpt(script)
			case "write_stdin":
				calls[callID] = t.activityShellSessions[strings.TrimSpace(string(args["session_id"]))]
			}
		} else if kind == "function_call_output" || kind == "custom_tool_call_output" {
			if call, ok := cellCalls[callID]; ok {
				delete(cellCalls, callID)
				if texts := executionOutputTexts(item["output"]); len(texts) > 0 {
					status, cell, _ := codeModeExecutionHeader(texts[0])
					if status == "running" && (call.cell == "" || call.cell == cell) && len(t.activityCellOperations) < 1024 {
						t.activityCellOperations[cell] = call.operation
					} else if status != "" && status != "running" {
						delete(t.activityCellOperations, call.cell)
					}
				}
			}
			excerpt := calls[callID]
			delete(calls, callID)
			if excerpt != "" && len(t.activityShellSessions) < 1024 {
				if session := toolActivityOutputSession(item["output"]); session != "" {
					t.activityShellSessions[session] = excerpt
				}
			}
		}
	}
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
