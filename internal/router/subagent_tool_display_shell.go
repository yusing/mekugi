package router

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

func toolActivityShellLanguage(script, language string) string {
	if shellsyntax.IsBatch(script) {
		programs, err := shellsyntax.Split(script)
		if err != nil {
			return "Run\n" + toolActivityFenced("", script)
		}
		if len(programs) > 1 {
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
	if err == nil && len(parsed.Interpreter) == 1 &&
		(parsed.Interpreter[0] == "bash" || parsed.Interpreter[0] == "sh") {
		if summary, ok := toolActivityReads(parsed.Body); ok {
			return summary
		}
	}
	if strings.TrimSpace(script) == "" {
		return "Run"
	}
	if projection, ok := shellInterpreterScriptProjection(script); ok {
		return "Run\n" + toolActivityFenced(projection.Language, projection.Source)
	}
	if err != nil || len(parsed.Interpreter) == 0 {
		language = ""
	} else if parsed.Interpreter[0] != "bash" {
		language = parsed.Interpreter[0]
	}
	language = toolActivityLanguage(language)
	return "Run\n" + toolActivityFenced(language, script)
}

func toolActivityLanguage(interpreter string) string {
	if strings.TrimSpace(interpreter) == "" {
		return ""
	}
	language := shellsyntax.InterpreterIdentity(interpreter)
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
	case "psql", "mysql", "sqlite":
		language = "sql"
	}
	// Interpreter filenames need not be safe Markdown info strings.
	if strings.ContainsAny(language, "`~ \t\r\n") {
		language = ""
	}
	return language
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
	type statementDisplay struct {
		display   string
		ok        bool
		separator bool
	}
	statements := make([]statementDisplay, len(program.Stmts))
	for index, statement := range program.Stmts {
		statements[index].display, statements[index].ok = toolActivityStatement(script, statement)
		statements[index].separator = toolActivityReadSeparator(statement)
	}
	var displays []string
	classified := false
	for _, result := range statements {
		classified = classified || result.ok
	}
	for index, statement := range program.Stmts {
		result := statements[index]
		if result.separator && classified {
			continue
		}
		display, ok := result.display, result.ok
		if ok {
			classified = true
			if display == "" {
				continue
			}
		} else {
			start, end := int(statement.Pos().Offset()), toolActivityStatementDisplayEnd(script, statement)
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

// A statement's End includes its separator. Keep that separator in the
// executed source, but omit a cosmetic trailing semicolon from a per-command
// activity preview. Other terminators (notably &) remain visible.
func toolActivityStatementDisplayEnd(script string, statement *syntax.Stmt) int {
	end := int(statement.End().Offset())
	offset := int(statement.Semicolon.Offset())
	if statement.Semicolon.IsValid() && offset < len(script) && script[offset] == ';' {
		return offset
	}
	return end
}

// Literal section headings alongside classified operations are decoration.
// Keep dynamic output, redirections, and headings-only scripts visible.
func toolActivityReadSeparator(statement *syntax.Stmt) bool {
	argv, ok := toolActivityLiteralCall(statement)
	if !ok || len(argv) < 2 || argv[0] != "printf" {
		return false
	}
	heading := argv[1]
	if len(argv) == 3 && (heading == `%s\n` || heading == `\n%s\n`) {
		heading = argv[2]
	} else if len(argv) != 2 || strings.Contains(heading, "%") {
		return false
	}
	heading = strings.TrimSpace(strings.ReplaceAll(heading, `\n`, "\n"))
	if strings.ContainsAny(heading, "\r\n\\") {
		return false
	}
	for _, border := range []string{"---", "===", "###"} {
		if strings.HasPrefix(heading, border) && strings.HasSuffix(heading, border) {
			return true
		}
	}
	return false
}

// Recognize only transparent search bounds and executable lookups. Keep their
// complete source, including redirections and guards, rather than implying that
// a pipeline's stages are independent operations.
func toolActivityStatement(script string, statement *syntax.Stmt) (string, bool) {
	if statement.Background || statement.Negated || statement.Coprocess || statement.Disown {
		return "", false
	}
	if binary, ok := statement.Cmd.(*syntax.BinaryCmd); ok {
		if binary.Op == syntax.AndStmt && len(statement.Redirs) == 0 {
			left, leftOK := toolActivityStatement(script, binary.X)
			right, rightOK := toolActivityStatement(script, binary.Y)
			if leftOK || rightOK {
				if !leftOK {
					left = toolActivityUnclassifiedShell(script[int(binary.X.Pos().Offset()):toolActivityStatementDisplayEnd(script, binary.X)])
				}
				if !rightOK {
					right = toolActivityUnclassifiedShell(script[int(binary.Y.Pos().Offset()):toolActivityStatementDisplayEnd(script, binary.Y)])
				}
				return strings.Trim(strings.Join([]string{left, right}, "\n\n"), "\n"), true
			}
		}
		if display, ok := toolActivityNumberedRead(script, statement, binary); ok {
			return display, true
		}
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
		if binary.Op == syntax.Pipe && len(statement.Redirs) == 0 && (strings.HasPrefix(left, "Search ") || strings.HasPrefix(left, "Search\n") || strings.HasPrefix(left, "List ")) {
			filter, search := toolActivityStatement(script, binary.Y)
			if toolActivitySearchFilter(argv) {
				return left, true
			}
			if search && (strings.HasPrefix(filter, "Search ") || strings.HasPrefix(filter, "Search\n")) {
				return left + "\n\n" + filter, true
			}
		}
		if binary.Op == syntax.Pipe && len(statement.Redirs) == 0 && len(argv) > 0 && argv[0] == "head" && toolActivitySearchFilter(argv) {
			if source, valid := toolActivityPatternCall(script, binary.X); valid && source[0] == "cat" &&
				(strings.HasPrefix(left, "Read ") || strings.HasPrefix(left, "Skill Read ")) {
				return left, true
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
		if command != "find" && command != "rg" && command != "grep" {
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
	return display, ok
}

// Recognize the common line-numbered source read produced by `nl -ba FILE |
// sed -n RANGES`. Both sides are deliberately strict: other nl numbering modes
// do not preserve a one-to-one mapping between source and output line numbers,
// and arbitrary sed programs may do more than select bounded source lines.
func toolActivityNumberedRead(script string, statement *syntax.Stmt, binary *syntax.BinaryCmd) (string, bool) {
	if binary.Op != syntax.Pipe || len(statement.Redirs) != 0 {
		return "", false
	}
	left, leftOK := toolActivityPatternCall(script, binary.X)
	right, rightOK := toolActivityLiteralCall(binary.Y)
	if !leftOK || !rightOK || len(left) != 3 || left[0] != "nl" || left[1] != "-ba" ||
		left[2] == "" || strings.HasPrefix(left[2], "-") || len(right) != 3 ||
		right[0] != "sed" || right[1] != "-n" {
		return "", false
	}
	spans, ok := toolActivityPrintSpans(right[2])
	if !ok {
		return "", false
	}
	return "Read " + toolActivityCode(left[2]+" "+strings.Join(spans, " ")), true
}

func toolActivityLiteralCall(statement *syntax.Stmt) ([]string, bool) {
	return toolActivityPatternCall("", statement)
}

// Preserve patterns as source, without expansion or executable substitutions.
func toolActivityPatternCall(script string, statement *syntax.Stmt) ([]string, bool) {
	if statement == nil || len(statement.Redirs) != 0 || statement.Background || statement.Negated ||
		statement.Coprocess || statement.Disown {
		return nil, false
	}
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Assigns) != 0 {
		return nil, false
	}
	argv := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		value, literal := shellCatLiteral(arg)
		if !literal {
			if script == "" || !toolActivityPatternWord(arg) {
				return nil, false
			}
			value = script[int(arg.Pos().Offset()):int(arg.End().Offset())]
		}
		argv = append(argv, value)
	}
	return argv, true
}

func toolActivityPrintSpans(program string) ([]string, bool) {
	commands := strings.Split(program, ";")
	spans := make([]string, 0, len(commands))
	for _, command := range commands {
		addresses, printOnly := strings.CutSuffix(command, "p")
		start, end, hasRange := strings.Cut(addresses, ",")
		if !printOnly || !hasRange || strings.Trim(start, "0123456789") != "" ||
			strings.Trim(end, "0123456789") != "" {
			return nil, false
		}
		first, firstErr := strconv.Atoi(start)
		last, lastErr := strconv.Atoi(end)
		if firstErr != nil || lastErr != nil || first < 1 || last < first {
			return nil, false
		}
		spans = append(spans, start+":"+end)
	}
	return spans, len(spans) != 0
}

// Omit output-only pipeline helpers from the operation label. File operands,
// output-file flags, and dynamic bounds are not transparent output filters.
func toolActivitySearchFilter(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch argv[0] {
	case "head", "tail":
		if len(argv) == 1 {
			return true
		}
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
	patterns := command == "rg" || command == "grep" || command == "ls" ||
		command == "cat" || command == "mcat" || command == "sed" || command == "nl" || command == "inspect_file"
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
	case "mread":
		return "", true
	case "cat":
		if len(argv) < 2 {
			return "", false
		}
		for _, path := range argv[1:] {
			if path == "" || strings.HasPrefix(path, "-") {
				return "", false
			}
			label, value := "Read", path
			if filepath.Base(path) == "SKILL.md" && filepath.Dir(path) != "." {
				label, value = "Skill Read", filepath.Base(filepath.Dir(path))
			}
			add(label, value)
		}
	case "mcat":
		specs, _, err := parseReadBundle(argv[1:])
		if err != nil {
			return "", false
		}
		for _, spec := range specs {
			label, value := "Read", spec.path
			if filepath.Base(spec.path) == "SKILL.md" && filepath.Dir(spec.path) != "." {
				label, value = "Skill Read", filepath.Base(filepath.Dir(spec.path))
			}
			if spec.span != "" {
				value += " " + spec.span
			}
			add(label, value)
		}
	case "nl":
		if len(argv) != 3 || argv[1] != "-ba" || argv[2] == "" || strings.HasPrefix(argv[2], "-") {
			return "", false
		}
		add("Read", argv[2])
	case "sed":
		// Only a literal, bounded print is a read preview, not arbitrary
		// sed programs, in-place edits, or input from stdin.
		if len(argv) != 4 || argv[1] != "-n" || argv[3] == "" || strings.HasPrefix(argv[3], "-") {
			return "", false
		}
		spans, ok := toolActivityPrintSpans(argv[2])
		if !ok {
			return "", false
		}
		add("Read", argv[3]+" "+strings.Join(spans, " "))
	case "inspect_file":
		options := true
		seen := make(map[string]bool)
		for i := 1; i < len(argv); i++ {
			arg := argv[i]
			if options && arg == "--" {
				options = false
				continue
			}
			if options && strings.HasPrefix(arg, "-") {
				name, value, inline := strings.Cut(arg, "=")
				if seen[name] {
					return "", false
				}
				seen[name] = true
				if name == "--json" && !inline {
					continue
				}
				if name != "--max-tokens" {
					return "", false
				}
				if !inline {
					i++
					if i >= len(argv) {
						return "", false
					}
					value = argv[i]
				}
				if _, valid := toolActivityPositiveDecimal(value, maxOutputTokens); !valid {
					return "", false
				}
				continue
			}
			if arg == "" || strings.ContainsRune(arg, '\x00') {
				return "", false
			}
			add("Inspect", arg)
		}
		if len(operations) == 0 {
			return "", false
		}
	case "msymbol":
		var operands []string
		seenOptions := make(map[string]bool)
		for index := 1; index < len(argv); index++ {
			name, value, inline := strings.Cut(argv[index], "=")
			if name == "--workspace" || name == "--max-tokens" {
				if inline && name != "--max-tokens" || seenOptions[name] {
					return "", false
				}
				seenOptions[name] = true
				if !inline {
					index++
					if index >= len(argv) {
						return "", false
					}
					value = argv[index]
				}
				if value == "" {
					return "", false
				}
				if name == "--max-tokens" {
					if _, valid := toolActivityPositiveDecimal(value, maxOutputTokens); !valid {
						return "", false
					}
				}
				continue
			}
			operands = append(operands, argv[index])
		}
		label := "Read"
		if len(operands) == 0 {
			return "", false
		}
		for index := 0; index < len(operands); {
			mode := operands[index]
			if (mode != "def" && mode != "refs") || index+2 >= len(operands) {
				return "", false
			}
			if mode == "refs" {
				label = "Search"
			}
			inputPath := operands[index+1]
			if inputPath == "" {
				return "", false
			}
			index += 2
			_, row, combined := strings.CutLast(inputPath, ":")
			_, hasLine := toolActivityPositiveDecimal(row, 1<<53-1)
			hasLine = combined && hasLine
			if !hasLine && index < len(operands) {
				if _, valid := toolActivityPositiveDecimal(operands[index], 1<<53-1); valid {
					hasLine = true
					index++
				}
			}
			if mode == "refs" && !hasLine || index >= len(operands) || operands[index] == "" {
				return "", false
			}
			index++ // symbol
			if index < len(operands) && operands[index] != "def" && operands[index] != "refs" {
				if _, valid := toolActivityPositiveDecimal(operands[index], 1<<53-1); !valid {
					return "", false
				}
				index++
			}
		}
		add(label, script[int(call.Args[1].Pos().Offset()):int(call.End().Offset())])

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
		if len(argv) < 2 {
			return "", false
		}
		add("Search", script[int(call.Args[1].Pos().Offset()):int(call.End().Offset())])
	case "rg", "grep":
		return toolActivitySearch(argv)
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

// Match positive, canonical decimal read options without executing them.
func toolActivityPositiveDecimal(value string, maximum uint64) (uint64, bool) {
	if value == "" || value[0] == '0' || strings.Trim(value, "0123456789") != "" {
		return 0, false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return number, err == nil && number <= maximum
}

func toolActivityUnclassifiedShell(source string) string {
	if strings.ContainsAny(source, "\r\n") {
		return toolActivityShell(source)
	}
	return "Run " + toolActivityCode(source)
}
