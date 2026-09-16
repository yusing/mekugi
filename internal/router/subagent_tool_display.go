package router

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// Presentation only: never evaluate code, expand paths, or alter the observed call.
func subagentToolPreview(item map[string]json.RawMessage, qualifiedName string, shellDisplay func(map[string]json.RawMessage, string) (string, bool)) string {
	name := jsonString(item, "name")
	// Agent messages already have a dedicated commentary render.
	if name == "send_message" && commentaryExcluded(jsonString(item, "namespace"), name) {
		return ""
	}
	if commentaryExcluded(jsonString(item, "namespace"), name) {
		return "Tool call: " + commentaryCode(qualifiedName)
	}
	if shellDisplay != nil {
		if display, ok := shellDisplay(item, qualifiedName); ok {
			return display
		}
	}
	input := jsonString(item, "arguments")
	if input == "" {
		input = jsonString(item, "input")
	}
	shortName := strings.TrimPrefix(qualifiedName, "functions.")
	if server, ok := strings.CutPrefix(jsonString(item, "namespace"), "mcp__"); ok && server != "" && name != "" {
		return toolActivityDetail("MCP "+commentaryCode(server+"."+name), input)
	}
	if server, tool, ok := toolActivityMCPName(shortName); ok {
		// Source: codex-rs/tui/src/history_cell/mcp.rs:727:748 format_mcp_invocation.
		// Keep Codex's server.tool identity and arguments, without claiming success.
		return toolActivityDetail("MCP "+commentaryCode(server+"."+tool), input)
	}
	if shortName == "exec" {
		if calls, ok := toolActivityUnwrapExecCalls(input, false); ok {
			var displays []string
			for _, nested := range calls {
				if display := subagentToolPreview(nested, qualifiedToolName(jsonString(nested, "namespace"), jsonString(nested, "name")), shellDisplay); display != "" {
					displays = append(displays, display)
				}
			}
			return strings.Join(displays, "\n\n")
		}
	}
	var arguments map[string]json.RawMessage
	_ = json.Unmarshal([]byte(input), &arguments)
	// Cell waits use the operation-aware display below, not the generic helper label.
	if label := toolActivityBuiltinLabel(shortName); label != "" && (shortName != "wait" || arguments["cell_id"] == nil) {
		return toolActivityDetail(label, input)
	}
	if kind := jsonString(item, "type"); kind == "local_shell_call" || kind == "shell_call" {
		var action struct {
			Command  []string `json:"command"`
			Commands []string `json:"commands"`
		}
		if json.Unmarshal(item["action"], &action) == nil {
			if len(action.Commands) > 0 {
				return toolActivityShell(strings.Join(action.Commands, "\n"))
			}
			if len(action.Command) > 0 {
				return toolActivityShellArgv(action.Command)
			}
		}
		return "Run"
	}
	switch shortName {
	case "shell", "shell_command", "exec_command":
		script := input
		if arguments != nil {
			script = jsonString(arguments, "cmd")
			if script == "" {
				script = jsonString(arguments, "command")
			}
			var argv []string
			if json.Unmarshal(arguments["command"], &argv) == nil {
				return toolActivityShellArgv(argv)
			}
		}
		return toolActivityShell(script)
	case "exec":
		return toolActivityJavaScript(input)
	case "wait":
		if jsonString(arguments, "cell_id") != "" {
			var terminate bool
			_ = json.Unmarshal(arguments["terminate"], &terminate)
			if terminate {
				return "Stop · operation unavailable"
			}
			return "Still Running · operation unavailable"
		}
	case "view_image":
		return toolActivityDetail("View image", jsonString(arguments, "path"))
	case "write_stdin":
		return toolActivityWriteStdin(arguments)
	case "apply_patch", "hpatch", "hpatch_recover":
		// Edit evidence belongs in host tool results and the live diff viewer,
		// not a second generated commentary rendering.
		return ""
	}
	switch jsonString(item, "type") {
	case "web_search_call":
		var action map[string]json.RawMessage
		_ = json.Unmarshal(item["action"], &action)
		switch jsonString(action, "type") {
		case "search":
			query := jsonString(action, "query")
			var queries []string
			if query == "" && json.Unmarshal(action["queries"], &queries) == nil {
				query = strings.Join(queries, "\n")
			}
			return toolActivityDetail("Search web", query)
		case "open_page":
			return toolActivityDetail("Open page", jsonString(action, "url"))
		case "find":
			return toolActivityDetail("Find in page", jsonString(action, "pattern"))
		}
		return "Search web"
	case "file_search_call":
		var queries []string
		_ = json.Unmarshal(item["queries"], &queries)
		return toolActivityDetail("Search files", strings.Join(queries, "\n"))
	case "image_generation_call":
		return "Generate image"
	case "code_interpreter_call":
		return toolActivityDetail("Run code", jsonString(item, "code"))
	}
	return toolActivityDetail("Tool call: "+commentaryCode(qualifiedName), input)
}

// Use one label table for native calls and normalized Code Mode identifiers.
// Retain full arguments: auxiliary options are part of the observed operation too.
func toolActivityBuiltinLabel(name string) string {
	switch name {
	case "list_mcp_resources":
		return "List MCP resources"
	case "list_mcp_resource_templates":
		return "List MCP resource templates"
	case "read_mcp_resource":
		return "Read MCP resource"
	case "clock.curr_time", "clock__curr_time", "curr_time":
		return "Read current time"
	case "clock.sleep", "clock__sleep", "sleep":
		return "Sleep"
	case "get_context_remaining":
		return "Check remaining context"
	case "new_context":
		return "Start new context"
	case "create_goal":
		return "Create goal"
	case "get_goal":
		return "Read goal"
	case "update_goal":
		return "Update goal"
	case "web.run", "web__run":
		return "Browse web"
	case "image_gen.imagegen", "image_gen__imagegen":
		return "Generate image"
	case "wait":
		return "Wait for execution"
	}
	return ""
}

func toolActivityMCPName(name string) (server, tool string, ok bool) {
	qualified, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return "", "", false
	}
	server, tool, ok = strings.Cut(qualified, "__")
	return server, tool, ok && server != "" && tool != ""
}

func toolActivityDetail(label, input string) string {
	if strings.TrimSpace(input) == "" {
		return label
	}
	return label + "\n" + toolActivityCode(input)
}

func toolActivityFenced(language, input string) string {
	fence := "```"
	for strings.Contains(input, fence) {
		fence += "`"
	}
	trailer := "\n"
	if strings.HasSuffix(input, "\n") {
		trailer = ""
	}
	return fence + language + "\n" + input + trailer + fence
}

func toolActivityJavaScript(input string) string {
	if strings.TrimSpace(input) == "" {
		return "Run JavaScript"
	}
	return "Run JavaScript\n" + toolActivityFenced("javascript", input)
}

func toolActivityWriteStdin(arguments map[string]json.RawMessage) string {
	chars := jsonString(arguments, "chars")
	if chars != "" {
		return toolActivityDetail("Send input", chars)
	}
	return "Still Running · command unavailable"
}

func toolActivityShellArgv(argv []string) string {
	if len(argv) == 0 {
		return "Run"
	}
	if len(argv) == 3 && (filepath.Base(argv[0]) == "bash" || filepath.Base(argv[0]) == "sh") &&
		(argv[1] == "-c" || argv[1] == "-lc") {
		return toolActivityShellLanguage(argv[2], filepath.Base(argv[0]))
	}
	return toolActivityShell(workerCommand(argv[0], argv[1:]))
}

func toolActivityShell(script string) string {
	return toolActivityShellLanguage(script, "bash")
}
