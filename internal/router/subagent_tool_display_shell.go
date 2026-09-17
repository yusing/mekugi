package router

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

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
	if err == nil && parsed.CommandTemplate == "" && len(parsed.Interpreter) == 1 &&
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
