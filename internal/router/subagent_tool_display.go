package router

import (
	"encoding/json"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// Presentation only: never evaluate code, expand paths, or alter the observed call.
func subagentToolActivityTexts(item map[string]json.RawMessage, qualifiedName string, history *mekugiHistory, shellDisplay func(map[string]json.RawMessage, string) (string, bool)) []string {
	name := jsonString(item, "name")
	// Agent messages already have a dedicated commentary render.
	if name == "send_message" && commentaryExcluded(jsonString(item, "namespace"), name) {
		return nil
	}
	if commentaryExcluded(jsonString(item, "namespace"), name) {
		return []string{"Tool call: " + commentaryCode(qualifiedName)}
	}
	if shellDisplay != nil {
		if display, ok := shellDisplay(item, qualifiedName); ok {
			return []string{display}
		}
	}
	input := jsonString(item, "arguments")
	if input == "" {
		input = jsonString(item, "input")
	}
	shortName := strings.TrimPrefix(qualifiedName, "functions.")
	if server, ok := strings.CutPrefix(jsonString(item, "namespace"), "mcp__"); ok && server != "" && name != "" {
		return []string{toolActivityDetail("MCP "+commentaryCode(server+"."+name), input)}
	}
	if server, tool, ok := toolActivityMCPName(shortName); ok {
		// Source: codex-rs/tui/src/history_cell/mcp.rs:727:748 format_mcp_invocation.
		// Keep Codex's server.tool identity and arguments, without claiming success.
		return []string{toolActivityDetail("MCP "+commentaryCode(server+"."+tool), input)}
	}
	if shortName == "exec" {
		if calls, ok := toolActivityUnwrapExecCalls(input, false); ok {
			var displays []string
			for _, nested := range calls {
				displays = append(displays, subagentToolActivityTexts(nested, qualifiedToolName(jsonString(nested, "namespace"), jsonString(nested, "name")), nil, shellDisplay)...)
			}
			// Keep patch files independently renderable under their existing delivery budget.
			hasPatch := slices.ContainsFunc(calls, func(call map[string]json.RawMessage) bool {
				return jsonString(call, "name") == "apply_patch"
			})
			if len(calls) > 1 && !hasPatch {
				return []string{strings.Join(displays, "\n\n")}
			}
			return displays
		}
	}
	var arguments map[string]json.RawMessage
	_ = json.Unmarshal([]byte(input), &arguments)
	// Cell waits use the operation-aware display below, not the generic helper label.
	if label := toolActivityBuiltinLabel(shortName); label != "" && (shortName != "wait" || arguments["cell_id"] == nil) {
		return []string{toolActivityDetail(label, input)}
	}
	if kind := jsonString(item, "type"); kind == "local_shell_call" || kind == "shell_call" {
		var action struct {
			Command  []string `json:"command"`
			Commands []string `json:"commands"`
		}
		if json.Unmarshal(item["action"], &action) == nil {
			if len(action.Commands) > 0 {
				return []string{toolActivityShell(strings.Join(action.Commands, "\n"))}
			}
			if len(action.Command) > 0 {
				return []string{toolActivityShellArgv(action.Command)}
			}
		}
		return []string{"Run"}
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
				return []string{toolActivityShellArgv(argv)}
			}
		}
		return []string{toolActivityShell(script)}
	case "exec":
		return []string{toolActivityJavaScript(input)}
	case "wait":
		if jsonString(arguments, "cell_id") != "" {
			var terminate bool
			_ = json.Unmarshal(arguments["terminate"], &terminate)
			if terminate {
				return []string{"Stop · operation unavailable"}
			}
			return []string{"Still Running · operation unavailable"}
		}
	case "view_image":
		return []string{toolActivityDetail("View image", jsonString(arguments, "path"))}
	case "write_stdin":
		return []string{toolActivityWriteStdin(arguments)}
	case "apply_patch":
		patch := input
		if arguments != nil {
			if decoded := jsonString(arguments, "patch"); decoded != "" {
				patch = decoded
			} else if decoded := jsonString(arguments, "input"); decoded != "" {
				patch = decoded
			}
		}
		return toolActivityPatch(patch)
	case "hpatch", "hpatch_recover":
		if history != nil && history.TranslationError == "" && history.Patch != "" {
			return toolActivityPatch(history.Patch)
		}
		return []string{toolActivityDetail("Edit", input)}
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
			return []string{toolActivityDetail("Search web", query)}
		case "open_page":
			return []string{toolActivityDetail("Open page", jsonString(action, "url"))}
		case "find":
			return []string{toolActivityDetail("Find in page", jsonString(action, "pattern"))}
		}
		return []string{"Search web"}
	case "file_search_call":
		var queries []string
		_ = json.Unmarshal(item["queries"], &queries)
		return []string{toolActivityDetail("Search files", strings.Join(queries, "\n"))}
	case "image_generation_call":
		return []string{"Generate image"}
	case "code_interpreter_call":
		return []string{toolActivityDetail("Run code", jsonString(item, "code"))}
	}
	return []string{toolActivityDetail("Tool call: "+commentaryCode(qualifiedName), input)}
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

func toolActivityDiff(label, patch string) string {
	if strings.TrimSpace(patch) == "" {
		return label
	}
	return label + "\n" + toolActivityFenced("diff", patch)
}

func toolActivityPatch(patch string) []string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(patch, "\r\n", "\n")), "\n")
	if len(lines) < 2 || lines[0] != "*** Begin Patch" || lines[len(lines)-1] != "*** End Patch" {
		return []string{toolActivityDiff("Edit", patch)}
	}
	var displays, body []string
	label := ""
	flush := func() {
		if label != "" {
			displays = append(displays, toolActivityDiff(label, strings.Join(body, "\n")))
		}
		body = nil
	}
	for _, line := range lines[1 : len(lines)-1] {
		kind, path, _ := strings.Cut(line, ": ")
		switch kind {
		case "*** Add File", "*** Update File", "*** Delete File":
			if path == "" {
				return []string{toolActivityDiff("Edit", patch)}
			}
			flush()
			operation := map[string]string{"*** Add File": "Write", "*** Update File": "Edit", "*** Delete File": "Delete"}[kind]
			label = operation + " " + commentaryCode(path)
		case "*** Move to":
			if label == "" || path == "" {
				return []string{toolActivityDiff("Edit", patch)}
			}
			label = "Move " + strings.TrimPrefix(label, "Edit ") + " → " + commentaryCode(path)
		case "*** End of File":
			// Patch metadata, not a line in the edited file.
		default:
			if label == "" {
				return []string{toolActivityDiff("Edit", patch)}
			}
			body = append(body, line)
		}
	}
	flush()
	if len(displays) == 0 {
		return []string{"Edit"}
	}
	return displays
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

func toolActivityUnwrapShell(script, language string) (string, string) {
	// Native carriers may wrap the source in `shell bash $'...'`.
	for range 2 {
		program, err := syntax.NewParser().Parse(strings.NewReader(script), "")
		if err != nil || len(program.Stmts) != 1 {
			break
		}
		statement := program.Stmts[0]
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) != 3 || len(call.Assigns) != 0 || len(statement.Redirs) != 0 ||
			statement.Background || statement.Negated || statement.Coprocess || statement.Disown {
			break
		}
		command, a := shellCatLiteral(call.Args[0])
		interpreter, b := shellCatLiteral(call.Args[1])
		body, c := shellCatLiteral(call.Args[2])
		if !a || !b || !c || command != "shell" || (interpreter != "bash" && interpreter != "sh") {
			break
		}
		script = body
		language = interpreter
	}
	return script, language
}

func toolActivityShellLanguage(script, language string) string {
	script, language = toolActivityUnwrapShell(script, language)
	if toolActivityScriptReference(script) != "" {
		return "Running stored script · command unavailable"
	}
	if _, _, batch := shellsyntax.BatchHeader(script); batch {
		if programs, err := shellsyntax.Split(script); err == nil {
			displays := make([]string, 0, len(programs))
			for _, program := range programs {
				if display := toolActivityShellLanguage(program, language); display != "" {
					displays = append(displays, display)
				}
			}
			return strings.Join(displays, "\n\n")
		}
	}
	parsed, err := shellsyntax.Parse(script)
	if err == nil && !parsed.HasScript && parsed.CommandTemplate == "" && len(parsed.Interpreter) == 1 &&
		(parsed.Interpreter[0] == "bash" || parsed.Interpreter[0] == "sh") {
		if summary, ok := toolActivityReads(parsed.Body); ok {
			return summary
		}
	}
	if strings.TrimSpace(script) == "" {
		return "Run"
	}
	if err != nil || len(parsed.Interpreter) == 0 {
		language = ""
	} else if parsed.Interpreter[0] != "bash" {
		language = shellsyntax.InterpreterIdentity(parsed.Interpreter[0])
	}
	// Normalize known executable families, including versioned names such as
	// python3.12 and lua5.4. Leave other names intact for Codex's syntax lookup.
	switch strings.TrimRight(language, "0123456789.") {
	case "python", "pythonw", "pypy":
		language = "python"
	case "node", "nodejs", "bun", "deno", "qjs", "quickjs":
		language = "javascript"
	case "ts-node", "ts-node-esm", "tsx":
		language = "typescript"
	case "ruby", "jruby", "truffleruby":
		language = "ruby"
	case "perl":
		language = "perl"
	case "php":
		language = "php"
	case "lua", "luajit":
		language = "lua"
	case "tclsh", "wish":
		language = "tcl"
	case "rscript":
		language = "r"
	case "runghc", "runhaskell":
		language = "haskell"
	case "pwsh", "powershell":
		language = "powershell"
	case "ash", "dash", "ksh":
		language = "bash"
	case "gawk", "mawk", "nawk":
		language = "awk"
	}
	// Interpreter filenames need not be safe Markdown info strings.
	if strings.ContainsAny(language, "`~ \t\r\n") {
		language = ""
	}
	return "Run\n" + toolActivityFenced(language, script)
}

func toolActivityReads(script string) (string, bool) {
	program, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil || len(program.Stmts) == 0 {
		return "", false
	}
	// Heredoc bodies may occur after another statement on the same line,
	// outside Stmt.End(). Preserve the whole source rather than slicing them.
	hasHeredoc := false
	syntax.Walk(program, func(node syntax.Node) bool {
		if redirect, ok := node.(*syntax.Redirect); ok && redirect.Hdoc != nil {
			hasHeredoc = true
		}
		return !hasHeredoc
	})
	if hasHeredoc {
		var source strings.Builder
		offset := 0
		for _, statement := range program.Stmts {
			if display, ok := toolActivityStatement(script, statement); ok && display == "" {
				source.WriteString(script[offset:int(statement.Pos().Offset())])
				offset = int(statement.End().Offset())
			}
		}
		if offset == 0 {
			return "", false
		}
		source.WriteString(script[offset:])
		return "Run\n" + toolActivityFenced("bash", strings.Trim(source.String(), "\r\n")), true
	}
	var displays []string
	classified := false
	for _, statement := range program.Stmts {
		display, ok := toolActivityStatement(script, statement)
		if ok {
			classified = true
			if display == "" {
				continue
			}
		} else {
			start, end := int(statement.Pos().Offset()), int(statement.End().Offset())
			source := script[start:end]
			if strings.ContainsAny(source, "\r\n") {
				// Retain leading indentation, but not an earlier command on
				// the same line separated by a semicolon.
				lineStart := strings.LastIndex(script[:start], "\n") + 1
				if strings.Trim(script[lineStart:start], " \t") == "" {
					source = script[lineStart:end]
				}
				display = "Run\n" + toolActivityFenced("bash", source)
			} else {
				display = "Run " + toolActivityCode(source)
			}
		}
		displays = append(displays, display)
	}
	// Preserve the complete source and transport headers when there is
	// nothing to classify, rather than splitting an ordinary Run preview.
	if !classified {
		return "", false
	}
	return strings.Join(displays, "\n\n"), true
}

// Recognize only transparent search bounds and executable lookups. Keep their
// complete source, including redirections and guards, rather than implying that
// a pipeline's stages are independent operations.
func toolActivityStatement(script string, statement *syntax.Stmt) (string, bool) {
	if statement.Background || statement.Negated || statement.Coprocess || statement.Disown {
		return "", false
	}
	if binary, ok := statement.Cmd.(*syntax.BinaryCmd); ok {
		right, ok := binary.Y.Cmd.(*syntax.CallExpr)
		if !ok || len(right.Assigns) != 0 || len(binary.Y.Redirs) != 0 ||
			binary.Y.Background || binary.Y.Negated || binary.Y.Coprocess || binary.Y.Disown {
			return "", false
		}
		var argv []string
		for _, arg := range right.Args {
			value, literal := shellCatLiteral(arg)
			if !literal {
				if !toolActivityPatternWord(arg) {
					return "", false
				}
				value = script[int(arg.Pos().Offset()):int(arg.End().Offset())]
			}
			argv = append(argv, value)
		}
		left, ok := toolActivityStatement(script, binary.X)
		if !ok {
			return "", false
		}
		label := ""
		if binary.Op == syntax.Pipe && (strings.HasPrefix(left, "Search ") || strings.HasPrefix(left, "Search\n")) {
			filter, search := toolActivityStatement(script, binary.Y)
			if toolActivitySearchFilter(argv) || search &&
				(strings.HasPrefix(filter, "Search ") || strings.HasPrefix(filter, "Search\n")) {
				label = "Search"
			}
		}
		if binary.Op == syntax.OrStmt && strings.HasPrefix(left, "Inspect ") &&
			len(argv) == 1 && argv[0] == "true" {
			label = "Inspect"
		}
		if label != "" && len(statement.Redirs) == 0 {
			return label + " " + toolActivityCode(script[int(statement.Pos().Offset()):int(statement.End().Offset())]), true
		}
		return "", false
	}
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Assigns) != 0 {
		return "", false
	}
	command, literal := shellCatLiteral(call.Args[0])
	if literal && command == commentaryArgumentName && len(statement.Redirs) == 0 {
		// Expanding progress text can itself run commands. Do not hide that work.
		executable := false
		syntax.Walk(call, func(node syntax.Node) bool {
			switch node.(type) {
			case *syntax.CmdSubst, *syntax.ProcSubst:
				executable = true
			}
			return !executable
		})
		return "", !executable
	}
	if len(statement.Redirs) != 0 {
		// Only discarded stderr is transparent to these search previews.
		if command != "find" && command != "hgrep" && command != "rg" && command != "grep" {
			return "", false
		}
		for _, redirect := range statement.Redirs {
			path, literal := shellCatLiteral(redirect.Word)
			if redirect.Op != syntax.RdrOut || redirect.N == nil || redirect.N.Value != "2" || !literal || path != "/dev/null" {
				return "", false
			}
		}
	}
	display, ok := toolActivityReadCommand(script, call)
	if ok && len(statement.Redirs) != 0 {
		return "Search " + toolActivityCode(script[int(statement.Pos().Offset()):int(statement.End().Offset())]), true
	}
	return display, ok
}

// These filters retain the complete pipeline in the preview. File operands,
// output-file flags, and dynamic bounds are not transparent output filters.
func toolActivitySearchFilter(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch argv[0] {
	case "head", "tail":
		bound := ""
		if len(argv) == 3 && argv[1] == "-n" {
			bound = argv[2]
		} else if len(argv) == 2 {
			var option bool
			bound, option = strings.CutPrefix(argv[1], "-")
			if !option {
				return false
			}
		}
		_, valid := toolActivityPositiveDecimal(bound, 1<<53-1)
		return valid
	case "sort":
		for _, arg := range argv[1:] {
			flags, ok := strings.CutPrefix(arg, "-")
			if !ok || flags == "" || strings.Trim(flags, "nru") != "" {
				return false
			}
		}
		return true
	}
	return false
}

// Search and list details preserve shell patterns verbatim. Accept word
// patterns without expanding them; never treat substitutions as literal paths.
func toolActivityPatternWord(word *syntax.Word) bool {
	valid := true
	syntax.Walk(word, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted, nil:
		default:
			valid = false
		}
		return valid
	})
	return valid
}

func toolActivityReadCommand(script string, call *syntax.CallExpr) (string, bool) {
	var operations []struct{ label, detail string }
	add := func(label, detail string) {
		operations = append(operations, struct{ label, detail string }{label, detail})
	}
	command, literal := shellCatLiteral(call.Args[0])
	if !literal {
		return "", false
	}
	patterns := command == "rg" || command == "hgrep" || command == "grep" || command == "ls"
	var argv []string
	for _, arg := range call.Args {
		value, literal := shellCatLiteral(arg)
		if !literal {
			if !patterns || !toolActivityPatternWord(arg) {
				return "", false
			}
			value = script[int(arg.Pos().Offset()):int(arg.End().Offset())]
		}
		argv = append(argv, value)
	}
	switch argv[0] {
	case "cat", "hcat":
		if len(argv) < 2 {
			return "", false
		}
		paths, readRange := argv[1:], ""
		if argv[0] == "hcat" {
			pathIndex := 1
			seen := make(map[string]bool)
			for pathIndex < len(argv) && (argv[pathIndex] == "--max-tokens" || argv[pathIndex] == "--preview-bytes" || argv[pathIndex] == "--tail" || argv[pathIndex] == "-n") {
				option := argv[pathIndex]
				if option == "--tail" {
					if seen[option] {
						return "", false
					}
					seen[option] = true
					pathIndex++
					continue
				}
				if seen[option] || pathIndex+1 == len(argv) {
					return "", false
				}
				maximum := uint64(15500)
				if option == "-n" {
					maximum = 1<<53 - 1
				}
				if option == "--preview-bytes" {
					maximum = 65536
				}
				if _, valid := toolActivityPositiveDecimal(argv[pathIndex+1], maximum); !valid {
					return "", false
				}
				seen[option] = true
				pathIndex += 2
			}
			if seen["--tail"] && !seen["--max-tokens"] && !seen["-n"] {
				return "", false
			}
			if len(argv)-pathIndex != 1 && len(argv)-pathIndex != 2 {
				return "", false
			}
			paths = argv[pathIndex : pathIndex+1]
			if len(argv)-pathIndex == 2 {
				first, last, found := strings.Cut(argv[pathIndex+1], ":")
				start, validStart := toolActivityPositiveDecimal(first, 1<<53-1)
				end, validEnd := toolActivityPositiveDecimal(last, 1<<53-1)
				if !found || (!validStart && first != "0") || !validEnd || start > end {
					return "", false
				}
				readRange = " " + argv[pathIndex+1]
			}
		}
		for _, path := range paths {

			if path == "" || strings.HasPrefix(path, "-") {
				return "", false
			}
			label, value := "Read", path
			if filepath.Base(path) == "SKILL.md" && filepath.Dir(path) != "." {
				label, value = "Skill Read", filepath.Base(filepath.Dir(path))
			}
			add(label, value+readRange)
		}
	case "sed":
		// Only a literal, bounded print is a read preview, not arbitrary
		// sed programs, in-place edits, or input from stdin.
		if len(argv) != 4 || argv[1] != "-n" || argv[3] == "" || strings.HasPrefix(argv[3], "-") {
			return "", false
		}
		addresses, printOnly := strings.CutSuffix(argv[2], "p")
		start, end, hasRange := strings.Cut(addresses, ",")
		if !printOnly || !hasRange || strings.Trim(start, "0123456789") != "" || strings.Trim(end, "0123456789") != "" {
			return "", false
		}
		first, firstErr := strconv.Atoi(start)
		last, lastErr := strconv.Atoi(end)
		if firstErr != nil || lastErr != nil || first < 1 || last < first {
			return "", false
		}
		add("Read", argv[3]+" "+start+":"+end)
	case "inspect_file":
		if len(argv) != 2 || argv[1] == "" || strings.ContainsRune(argv[1], '\x00') {
			return "", false
		}
		path := filepath.Clean(argv[1])
		if path == "@shell" || strings.HasPrefix(path, "@shell"+string(filepath.Separator)) {
			return "", false
		}
		add("Inspect", argv[1])
	case "ls":
		detail := "."
		if len(argv) > 1 {
			detail = script[int(call.Args[1].Pos().Offset()):int(call.End().Offset())]
		}
		add("List", detail)
	case "command":
		if len(argv) != 3 || argv[1] != "-v" || argv[2] == "" || strings.HasPrefix(argv[2], "-") {
			return "", false
		}
		add("Inspect", script[int(call.Pos().Offset()):int(call.End().Offset())])
	case "find":
		for _, arg := range argv[1:] {
			switch arg {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
				return "", false
			}
		}
		fallthrough
	case "rg", "hgrep", "grep":
		if len(argv) < 2 {
			return "", false
		}
		// Keep all search flags and operands visible; do not guess which
		// operand is a query when an option may consume it.
		add("Search", script[int(call.Args[1].Pos().Offset()):int(call.End().Offset())])
	case "skills-mgr":
		if (len(argv) != 3 && len(argv) != 4) || argv[1] != "get" {
			return "", false
		}
		label := "Skill Read"
		if strings.Contains(argv[2], "/") {
			label = "Skill Reference Read"
		}
		detail := argv[2]
		if len(argv) == 4 {
			detail += " " + argv[3]
		}
		add(label, detail)

	default:
		return "", false
	}
	var lines []string
	for _, operation := range operations {
		separator := " "
		if strings.ContainsAny(operation.detail, "\r\n") {
			separator = "\n"
		}
		lines = append(lines, operation.label+separator+toolActivityCode(operation.detail))
	}
	return strings.Join(lines, "\n\n"), true

}

// Match the private readers' positive, canonical decimal options without executing them.
func toolActivityPositiveDecimal(value string, maximum uint64) (uint64, bool) {
	if value == "" || value[0] == '0' || strings.Trim(value, "0123456789") != "" {
		return 0, false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return number, err == nil && number <= maximum
}

// Recognize transparent Code Mode wrappers, not arbitrary programs containing a
// tool call (which may branch, execute other work, or never invoke that call).
// Output-only projections can describe commands but cannot identify sessions:
// their stdout is program-controlled, not execution metadata.
func toolActivityUnwrapExec(source string, requireResultMetadata bool) (map[string]json.RawMessage, bool) {
	calls, ok := toolActivityUnwrapExecCalls(source, requireResultMetadata)
	if !ok || len(calls) != 1 {
		return nil, false
	}
	return calls[0], true
}

func toolActivityUnwrapExecCalls(source string, requireResultMetadata bool) ([]map[string]json.RawMessage, bool) {
	parser := sitter.NewParser()
	defer parser.Close()
	if parser.SetLanguage(codeModeJavaScriptLanguage) != nil {
		return nil, false
	}
	bytes := []byte(source)
	tree := parser.Parse(bytes, nil)
	if tree == nil {
		return nil, false
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil, false
	}
	var statements []*sitter.Node
	for i := range root.NamedChildCount() {
		node := root.NamedChild(uint(i))
		if node.Kind() != "comment" {
			statements = append(statements, node)
		}
	}
	if len(statements) == 0 {
		return nil, false
	}
	var calls []map[string]json.RawMessage
	for i := 0; i < len(statements); i++ {
		first := statements[i]
		var expression *sitter.Node
		if first.Kind() == "expression_statement" {
			expression = first.NamedChild(0)
			if args, ok := toolActivityCallArguments(expression, bytes, "text"); ok && len(args) == 1 {
				expression = args[0]
			} else if args, ok := toolActivityCallArguments(expression, bytes, "generatedImage"); ok && len(args) == 1 && !requireResultMetadata {
				expression = args[0]
			} else if requireResultMetadata {
				return nil, false
			}
		} else if first.Kind() == "lexical_declaration" && first.NamedChildCount() == 1 && i+1 < len(statements) {
			declaration := first.NamedChild(0)
			binding := declaration.ChildByFieldName("name")
			if binding == nil || binding.Kind() != "identifier" {
				return nil, false
			}
			name := binding.Utf8Text(bytes)
			// A local runtime binding changes every call in the program, including earlier calls.
			if slices.Contains([]string{"tools", "text", "JSON", "Object", "Promise", "generatedImage"}, name) {
				return nil, false
			}
			if !toolActivityResultProjection(statements[i+1], bytes, name, requireResultMetadata) {
				return nil, false
			}
			expression = declaration.ChildByFieldName("value")
			i++
		}
		nested, ok := toolActivityAwaitedCalls(expression, bytes, requireResultMetadata)
		if !ok {
			return nil, false
		}
		calls = append(calls, nested...)
	}
	return calls, true
}

func toolActivityAwaitedCalls(expression *sitter.Node, bytes []byte, requireResultMetadata bool) ([]map[string]json.RawMessage, bool) {
	if expression == nil || expression.Kind() != "await_expression" {
		return nil, false
	}
	call := expression.NamedChild(0)
	if call == nil || call.Kind() != "call_expression" {
		return nil, false
	}
	if !requireResultMetadata {
		for _, method := range []string{"all", "allSettled"} {
			if args, ok := toolActivityCallArguments(call, bytes, "Promise", method); ok && len(args) == 1 && args[0].Kind() == "array" {
				var calls []map[string]json.RawMessage
				for i := range args[0].NamedChildCount() {
					node := args[0].NamedChild(uint(i))
					item, ok := toolActivityStaticToolCall(node, bytes)
					if !ok {
						return nil, false
					}
					calls = append(calls, item)
				}
				return calls, len(calls) > 0
			}
		}
	}
	item, ok := toolActivityStaticToolCall(call, bytes)
	if !ok {
		return nil, false
	}
	return []map[string]json.RawMessage{item}, true
}

func toolActivityStaticToolCall(call *sitter.Node, bytes []byte) (map[string]json.RawMessage, bool) {
	if call == nil || call.Kind() != "call_expression" {
		return nil, false
	}
	callee, args := call.ChildByFieldName("function"), call.ChildByFieldName("arguments")
	if callee == nil || args == nil || args.NamedChildCount() > 1 {
		return nil, false
	}
	property := callee.ChildByFieldName("property")
	if property == nil {
		return nil, false
	}
	name := property.Utf8Text(bytes)
	switch name {
	case "exec_command", "shell_command", "shell", "view_image", "write_stdin", "apply_patch":
	default:
		if _, _, ok := toolActivityMCPName(name); !ok && toolActivityBuiltinLabel(name) == "" {
			return nil, false
		}
	}
	if !toolActivityMemberPath(callee, bytes, "tools", name) || call.ChildByFieldName("optional_chain") != nil {
		return nil, false
	}
	var value any
	if args.NamedChildCount() == 1 {
		var ok bool
		value, ok = toolActivityStaticJavaScriptValue(args.NamedChild(0), bytes)
		if !ok {
			return nil, false
		}
	}
	item := map[string]json.RawMessage{"name": mustMarshalJSON(name)}
	if text, ok := value.(string); ok {
		item["input"] = mustMarshalJSON(text)
	} else if args.NamedChildCount() != 0 {
		item["arguments"] = mustMarshalJSON(string(mustMarshalJSON(value)))
	}
	return item, true
}

// Inspect the projection tree so formatting never determines whether a wrapper
// is transparent. Only the bound result and the supported metadata copy qualify.
func toolActivityResultProjection(statement *sitter.Node, source []byte, binding string, requireResultMetadata bool) bool {
	if statement.Kind() != "expression_statement" || statement.NamedChildCount() != 1 {
		return false
	}
	args, ok := toolActivityCallArguments(statement.NamedChild(0), source, "text")
	if !ok && !requireResultMetadata {
		args, ok = toolActivityCallArguments(statement.NamedChild(0), source, "generatedImage")
		return ok && len(args) == 1 && toolActivityMemberPath(args[0], source, binding)
	}
	if !ok || len(args) != 1 {
		return false
	}
	value := args[0]
	if toolActivityMemberPath(value, source, binding, "output") {
		return !requireResultMetadata
	}
	if toolActivityMemberPath(value, source, binding) {
		return true
	}
	args, ok = toolActivityCallArguments(value, source, "JSON", "stringify")
	if !ok || len(args) != 1 {
		return false
	}
	if toolActivityMemberPath(args[0], source, binding) {
		return true
	}
	args, ok = toolActivityCallArguments(args[0], source, "Object", "assign")
	if !ok || len(args) != 3 || !toolActivityMemberPath(args[1], source, binding) {
		return false
	}
	target, targetOK := toolActivityStaticJavaScriptValue(args[0], source)
	metadata, metadataOK := toolActivityStaticJavaScriptValue(args[2], source)
	targetObject, targetIsObject := target.(map[string]any)
	metadataObject, metadataIsObject := metadata.(map[string]any)
	return targetOK && metadataOK && targetIsObject && metadataIsObject &&
		len(targetObject) == 0 && len(metadataObject) == 1 && metadataObject["retained"] == false
}

func toolActivityCallArguments(node *sitter.Node, source []byte, path ...string) ([]*sitter.Node, bool) {
	if node == nil || node.Kind() != "call_expression" ||
		node.ChildByFieldName("optional_chain") != nil ||
		!toolActivityMemberPath(node.ChildByFieldName("function"), source, path...) {
		return nil, false
	}
	args := node.ChildByFieldName("arguments")
	if args == nil || args.Kind() != "arguments" {
		return nil, false
	}
	var values []*sitter.Node
	for i := range args.NamedChildCount() {
		child := args.NamedChild(uint(i))
		if child.Kind() != "comment" {
			values = append(values, child)
		}
	}
	return values, true
}

func toolActivityMemberPath(node *sitter.Node, source []byte, path ...string) bool {
	if node == nil || len(path) == 0 {
		return false
	}
	if len(path) == 1 {
		return node.Kind() == "identifier" && node.Utf8Text(source) == path[0]
	}
	if node.Kind() != "member_expression" || node.ChildByFieldName("optional_chain") != nil {
		return false
	}
	property := node.ChildByFieldName("property")
	return property != nil && property.Kind() == "property_identifier" &&
		property.Utf8Text(source) == path[len(path)-1] &&
		toolActivityMemberPath(node.ChildByFieldName("object"), source, path[:len(path)-1]...)
}

func toolActivityStaticJavaScriptValue(node *sitter.Node, source []byte) (any, bool) {
	if node == nil {
		return nil, false
	}
	switch node.Kind() {
	case "parenthesized_expression":
		if node.NamedChildCount() != 1 {
			return nil, false
		}
		return toolActivityStaticJavaScriptValue(node.NamedChild(0), source)
	case "object":
		value := make(map[string]any, node.NamedChildCount())
		for i := range node.NamedChildCount() {
			pair := node.NamedChild(uint(i))
			if pair.Kind() != "pair" {
				return nil, false
			}
			keyNode, valueNode := pair.ChildByFieldName("key"), pair.ChildByFieldName("value")
			if keyNode == nil || valueNode == nil {
				return nil, false
			}
			var key string
			switch keyNode.Kind() {
			case "property_identifier":
				key = keyNode.Utf8Text(source)
				if strings.ContainsRune(key, '\\') {
					return nil, false
				}
			case "string":
				var ok bool
				key, ok = toolActivityJavaScriptString(keyNode.Utf8Text(source))
				if !ok {
					return nil, false
				}
			default:
				return nil, false
			}
			decoded, ok := toolActivityStaticJavaScriptValue(valueNode, source)
			if !ok {
				return nil, false
			}
			value[key] = decoded
		}
		return value, true
	case "array":
		value := make([]any, 0, node.NamedChildCount())
		cursor := int(node.StartByte()) + 1
		for i := range node.NamedChildCount() {
			child := node.NamedChild(uint(i))
			gap := strings.TrimSpace(string(source[cursor:int(child.StartByte())]))
			if i == 0 && gap != "" || i > 0 && gap != "," || child.Kind() == "comment" {
				return nil, false
			}
			decoded, ok := toolActivityStaticJavaScriptValue(child, source)
			if !ok {
				return nil, false
			}
			value = append(value, decoded)
			cursor = int(child.EndByte())
		}
		tail := strings.TrimSpace(string(source[cursor : int(node.EndByte())-1]))
		if len(value) == 0 && tail != "" || len(value) != 0 && tail != "" && tail != "," {
			return nil, false
		}
		return value, true
	case "string":
		return toolActivityJavaScriptString(node.Utf8Text(source))
	case "number":
		return toolActivityJSONNumber(node.Utf8Text(source))
	case "true":
		return true, true
	case "false":
		return false, true
	case "null":
		return nil, true
	case "unary_expression":
		if node.NamedChildCount() != 1 || !strings.HasPrefix(strings.TrimSpace(node.Utf8Text(source)), "-") {
			return nil, false
		}
		child := node.NamedChild(0)
		if child.Kind() != "number" {
			return nil, false
		}
		return toolActivityJSONNumber(node.Utf8Text(source))
	default:
		return nil, false
	}
}
func toolActivityJSONNumber(source string) (any, bool) {
	if !json.Valid([]byte(source)) {
		return nil, false
	}
	value, err := strconv.ParseFloat(source, 64)
	if err != nil || math.IsInf(value, 0) {
		return nil, false
	}
	return value, true
}

func toolActivityJavaScriptString(source string) (string, bool) {
	if len(source) < 2 || source[0] != source[len(source)-1] || source[0] != '"' && source[0] != '\'' {
		return "", false
	}
	quote := source[0]
	rest := source[1 : len(source)-1]
	var value strings.Builder
	for rest != "" {
		if rest[0] == '\\' {
			if len(rest) < 2 {
				return "", false
			}
			switch rest[1] {
			case '\'', '"', '/', '\\':
				value.WriteByte(rest[1])
				rest = rest[2:]
				continue
			case 'b', 'f', 'n', 'r', 't', 'v', 'x', 'u':
			default:
				return "", false
			}
		}
		char, _, tail, err := strconv.UnquoteChar(rest, quote)
		if err != nil {
			return "", false
		}
		value.WriteRune(char)
		rest = tail
	}
	return value.String(), true
}
