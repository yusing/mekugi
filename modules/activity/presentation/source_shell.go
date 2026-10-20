package presentation

// Source: apps/mekugi/internal/router/subagent_tool_display_shell.go:112:280,313:504,539:592@0a356401.

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"mvdan.cc/sh/v3/syntax"
)

// ClassifyShell reports recognized Activity source, including mixed Run neighbors.
// A false result leaves the complete original program available to ShellSource.
func ClassifyShell(script, cwd string) (string, bool) {
	program, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil || len(program.Stmts) == 0 {
		return "", false
	}
	hasSetup := shellinstrumentation.HasShellSetup(program)
	// Heredoc bodies may occur after another statement on the same line,
	// outside Stmt.End(). Preserve the whole source rather than slicing them.
	hasHeredoc := false
	syntax.Walk(program, func(node syntax.Node) bool {
		if redirect, ok := node.(*syntax.Redirect); ok && redirect.Hdoc != nil {
			hasHeredoc = true
		}
		return !hasHeredoc
	})
	hasEdit := false
	if hasHeredoc || hasSetup {
		syntax.Walk(program, func(node syntax.Node) bool {
			if _, declaration := node.(*syntax.FuncDecl); declaration {
				return false
			}
			if statement, ok := node.(*syntax.Stmt); ok && !hasEdit {
				_, hasEdit = sourceEditStatement(script, shellsyntax.UnwrapTimeout(statement))
				if !hasEdit && hasHeredoc && !hasSetup {
					// A commit's heredoc is its message, not a program.
					_, hasEdit = VCSStatement(statement)
				}
			}
			return !hasEdit
		})
	}
	if (hasHeredoc || hasSetup) && !hasEdit {
		var source strings.Builder
		var timeouts []string
		offset := 0
		for _, statement := range program.Stmts {
			if display, ok := sourceStatement(script, statement, cwd); ok {
				for _, paragraph := range activitymodel.Paragraphs(display) {
					timeouts = append(timeouts, activitymodel.ParseOperation(paragraph).Timeouts...)
				}
				if !hasSetup && display == "" {
					source.WriteString(script[offset:int(statement.Pos().Offset())])
					offset = int(statement.End().Offset())
				}
			}
		}
		if offset == 0 && len(timeouts) == 0 {
			return "", false
		}
		source.WriteString(script[offset:])
		return activitymodel.AddTimeouts("Run\n"+markdownsyntax.SourceFence("bash", strings.Trim(source.String(), "\r\n")), timeouts), true
	}
	type statementDisplay struct {
		display   string
		ok        bool
		separator bool
	}
	statements := make([]statementDisplay, len(program.Stmts))
	for index, statement := range program.Stmts {
		statements[index].display, statements[index].ok = sourceStatement(script, statement, cwd)
		statements[index].separator = sourceReadSeparator(statement)
	}
	var displays []string
	classified, heading, operation := false, false, false
	for _, result := range statements {
		classified = classified || result.ok
		heading = heading || result.separator
		operation = operation || !result.separator
	}
	classified = classified || heading && operation
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
			start, end := int(statement.Pos().Offset()), sourceStatementDisplayEnd(script, statement)
			source := sourceStatementSource(script, statement)
			if strings.ContainsAny(source, "\r\n") {
				// Retain leading indentation, but not an earlier command on
				// the same line separated by a semicolon.
				lineStart := strings.LastIndex(script[:start], "\n") + 1
				if !hasHeredoc && strings.Trim(script[lineStart:start], " \t") == "" {
					source = script[lineStart:end]
				}
				display = "Run\n" + markdownsyntax.SourceFence("bash", source)
			} else {
				display = "Run " + SourceCode(source)
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

// Heredoc bodies are deferred past Stmt.End(), sometimes past a sibling on
// the same line. Print the owning AST instead of slicing off or borrowing bodies.
func sourceStatementSource(script string, statement *syntax.Stmt) string {
	return shellsyntax.StatementSource(statement, script[int(statement.Pos().Offset()):sourceStatementDisplayEnd(script, statement)])
}

// A statement's End includes its separator. Keep that separator in the
// executed source, but omit a cosmetic trailing semicolon from a per-command
// activity preview. Other terminators (notably &) remain visible.
func sourceStatementDisplayEnd(script string, statement *syntax.Stmt) int {
	end := int(statement.End().Offset())
	offset := int(statement.Semicolon.Offset())
	if statement.Semicolon.IsValid() && offset < len(script) && script[offset] == ';' {
		return offset
	}
	return end
}

// Literal section headings alongside other operations are decoration.
// Keep dynamic output, redirections, and headings-only scripts visible.
var activityPlainHeading = regexp.MustCompile(`^([A-Z][A-Za-z/-]*( [A-Za-z][A-Za-z/-]*)+|[A-Z][A-Z/-]+)$`)
var activitySuccessMarker = regexp.MustCompile(`^[A-Z][A-Za-z/-]*( [A-Za-z][A-Za-z/-]*)* (checks passed|evidence remains available)\.$`)

func sourceReadSeparator(statement *syntax.Stmt) bool {
	argv, ok := shellsyntax.PatternCall("", statement)
	if !ok || len(argv) < 2 {
		return false
	}
	heading := argv[1]
	switch {
	case argv[0] == "echo" && len(argv) == 2:
		// Escape handling differs between shells' echo builtins.
		if strings.Contains(heading, `\`) {
			return false
		}
	case argv[0] != "printf":
		return false
	case len(argv) == 3 && (heading == `%s\n` || heading == `\n%s\n`):
		heading = argv[2]
	case len(argv) != 2 || strings.Contains(heading, "%"):
		return false
	}
	// A bare title must be a blank-line-framed literal printf, with either
	// a capitalized multi-word title or one uppercase section label,
	// not a format with data arguments or an arbitrary output value.
	plainTitle := argv[0] == "printf" && len(argv) == 2 && strings.HasPrefix(heading, `\n`) && strings.HasSuffix(heading, `\n`)
	// Literal check/evidence confirmations are display-only status markers,
	// not arbitrary unframed prose or formats carrying values.
	successMarker := argv[0] == "printf" && len(argv) == 2 && strings.HasSuffix(heading, `\n`) && activitySuccessMarker.MatchString(strings.TrimSuffix(heading, `\n`))
	heading = strings.TrimSpace(strings.ReplaceAll(heading, `\n`, "\n"))
	if strings.ContainsAny(heading, "\r\n\\") {
		return false
	}
	for _, border := range []string{"---", "===", "###"} {
		if strings.HasPrefix(heading, border) && strings.HasSuffix(heading, border) {
			return true
		}
	}
	if successMarker || plainTitle && activityPlainHeading.MatchString(heading) {
		return true
	}
	return strings.HasSuffix(heading, ":") && strings.TrimSpace(strings.TrimSuffix(heading, ":")) != ""
}

// Recognize only transparent search bounds and executable lookups. Keep their
// complete source, including redirections and guards, rather than implying that
// a pipeline's stages are independent operations.
func sourceStatement(script string, statement *syntax.Stmt, cwd string) (result string, recognized bool) {
	original := statement
	timeouts := sourceTimeouts(statement)
	if binary, ok := statement.Cmd.(*syntax.BinaryCmd); ok && binary.Op == syntax.Pipe {
		timeouts = append(sourceTimeouts(binary.X), sourceTimeouts(binary.Y)...)
	}
	defer func() {
		if len(timeouts) == 0 {
			return
		}
		if !recognized {
			source := sourceTimeoutSource(script, original)
			result = "Run\n" + markdownsyntax.SourceFence("bash", source)
			recognized = true
		}
		result = activitymodel.AddTimeouts(result, timeouts)
	}()
	statement = shellsyntax.UnwrapTimeout(statement)
	if statement.Background || statement.Negated || statement.Coprocess || statement.Disown {
		return "", false
	}
	if inner := shellsyntax.UnwrapEnv(statement); inner != statement {
		return sourceStatement(script, inner, cwd)
	}
	if argv, ok := shellsyntax.PatternCall("", statement); ok && len(argv) == 3 &&
		(argv[0] == "bash" || argv[0] == "sh" || argv[0] == "dash") && (argv[1] == "-c" || argv[1] == "-lc") {
		display, recognized := ClassifyShell(argv[2], cwd)
		paragraphs := activitymodel.Paragraphs(display)
		if recognized && len(paragraphs) == 1 && activitymodel.ParseOperation(paragraphs[0]).Code == strings.Trim(argv[2], "\r\n") {
			// Setup and heredoc programs must stay whole, including the wrapper.
			innerTimeouts := activitymodel.ParseOperation(paragraphs[0]).Timeouts
			return activitymodel.AddTimeouts("Run\n"+markdownsyntax.SourceFence("bash", sourceStatementSource(script, original)), innerTimeouts), true
		}
		return display, recognized
	}
	if display, ok := sourceEditStatement(script, statement); ok {
		return display, true
	}
	if call, ok := VCSStatement(statement); ok {
		return call, true
	}
	if binary, ok := statement.Cmd.(*syntax.BinaryCmd); ok {
		if binary.Op == syntax.AndStmt && len(statement.Redirs) == 0 {
			left, leftOK := sourceStatement(script, binary.X, cwd)
			right, rightOK := sourceStatement(script, binary.Y, cwd)
			if leftOK || rightOK || sourceReadSeparator(binary.X) != sourceReadSeparator(binary.Y) {
				if !leftOK && !sourceReadSeparator(binary.X) {
					left = RunSource(sourceStatementSource(script, binary.X))
				}
				if !rightOK && !sourceReadSeparator(binary.Y) {
					right = RunSource(sourceStatementSource(script, binary.Y))
				}
				return strings.Trim(strings.Join([]string{left, right}, "\n\n"), "\n"), true
			}
		}
		if display, ok := sourcePipedRead(script, statement, binary, cwd); ok {
			return display, true
		}
		argv, ok := shellsyntax.PatternCall(script, binary.Y)
		if !ok {
			return "", false
		}
		left, ok := sourceStatement(script, binary.X, cwd)
		if !ok {
			return "", false
		}
		label := ""
		if binary.Op == syntax.Pipe && len(statement.Redirs) == 0 && (strings.HasPrefix(left, "Search ") || strings.HasPrefix(left, "Search\n") || strings.HasPrefix(left, "List ")) {
			filter, search := sourceStatement(script, binary.Y, cwd)
			if sourceSearchFilter(argv) {
				return left, true
			}
			if search && (strings.HasPrefix(filter, "Search ") || strings.HasPrefix(filter, "Search\n")) {
				return left + "\n\n" + filter, true
			}
		}
		if binary.Op == syntax.Pipe && len(statement.Redirs) == 0 && len(argv) > 0 && argv[0] == "head" && sourceSearchFilter(argv) {
			if source, valid := shellsyntax.PatternCall(script, binary.X); valid && source[0] == "cat" &&
				(strings.HasPrefix(left, "Read ") || strings.HasPrefix(left, "Skill ")) {
				return left, true
			}
		}
		if binary.Op == syntax.OrStmt && strings.HasPrefix(left, "Inspect ") &&
			len(argv) == 1 && argv[0] == "true" {
			label = "Inspect"
		}
		if label != "" && len(statement.Redirs) == 0 {
			return label + " " + SourceCode(script[int(statement.Pos().Offset()):int(statement.End().Offset())]), true
		}
		return "", false
	}
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Assigns) != 0 {
		return "", false
	}
	command, _ := shellsyntax.Literal(call.Args[0])
	command = shellsyntax.CommandName(command)
	if len(statement.Redirs) != 0 {
		// Only discarded stderr is transparent to these search and listing previews.
		if command != "find" && command != "rg" && command != "grep" && command != "ls" {
			return "", false
		}
		for _, redirect := range statement.Redirs {
			path, literal := shellsyntax.Literal(redirect.Word)
			if redirect.Op != syntax.RdrOut || redirect.N == nil || redirect.N.Value != "2" || !literal || path != "/dev/null" {
				return "", false
			}
		}
	}
	display, ok := sourceReadCommand(script, call, cwd)
	return display, ok
}

// Remove supported timeout prefixes from the preview, preserving the remaining
// source bytes, including quoting, redirections, and pipeline operators.
func sourceTimeoutSource(script string, statement *syntax.Stmt) string {
	type span struct{ start, end int }
	var spans []span
	heredoc := false
	syntax.Walk(statement, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CallExpr, *syntax.FuncDecl:
			return false
		}
		if redirect, ok := node.(*syntax.Redirect); ok && redirect.Hdoc != nil {
			heredoc = true
		}
		if stmt, ok := node.(*syntax.Stmt); ok {
			if inner := shellsyntax.UnwrapTimeout(stmt); inner != stmt {
				spans = append(spans, span{int(stmt.Cmd.(*syntax.CallExpr).Args[0].Pos().Offset()), int(inner.Cmd.(*syntax.CallExpr).Args[0].Pos().Offset())})
			}
		}
		return true
	})
	source := sourceStatementSource(script, statement)
	if heredoc {
		return source
	}
	start := int(statement.Pos().Offset())
	for _, span := range slices.Backward(spans) {
		source = source[:span.start-start] + source[span.end-start:]
	}
	return source
}

func sourceTimeouts(statement *syntax.Stmt) []string {
	if inner := shellsyntax.UnwrapEnv(statement); inner != statement {
		return sourceTimeouts(inner)
	}
	unwrapped := shellsyntax.UnwrapTimeout(statement)
	if unwrapped == statement {
		return nil
	}
	args := statement.Cmd.(*syntax.CallExpr).Args
	inner := unwrapped.Cmd.(*syntax.CallExpr).Args
	var timeouts []string
	for i := 1; i < len(args)-len(inner); i += 2 {
		value, _ := shellsyntax.Literal(args[i])
		timeouts = append(timeouts, value)
	}
	return append(timeouts, sourceTimeouts(unwrapped)...)
}

// A single-file cat or nl -ba preserves source line positions through a bounded
// sed print. Multiple inputs, other numbering modes, and arbitrary sed programs
// cannot be represented as the same source ranges.
func sourcePipedRead(script string, statement *syntax.Stmt, binary *syntax.BinaryCmd, cwd string) (string, bool) {
	if binary.Op != syntax.Pipe || len(statement.Redirs) != 0 {
		return "", false
	}
	left, leftOK := shellsyntax.PatternCall(script, binary.X)
	right, rightOK := shellsyntax.PatternCall("", binary.Y)
	if !leftOK || !rightOK || len(right) != 3 ||
		right[0] != "sed" || right[1] != "-n" {
		return "", false
	}
	path := ""
	switch {
	case len(left) == 3 && left[0] == "nl" && left[1] == "-ba":
		path = left[2]
	case len(left) == 2 && left[0] == "cat":
		path = left[1]
	}
	if path == "" || strings.HasPrefix(path, "-") {
		return "", false
	}
	spans, ok := sourcePrintSpans(right[2])
	if !ok {
		return "", false
	}
	return "Read " + SourceCode(pathdisplay.ForWorkspace(cwd, path)+" "+strings.Join(spans, " ")), true
}

func sourcePrintSpans(program string) ([]string, bool) {
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
func sourceSearchFilter(argv []string) bool {
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
		_, valid := sourcePositiveDecimal(bound, 1<<53-1)
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

// ShellSeparator reports a single display-only literal heading. Callers suppress
// it only alongside classified operations and retain failed or skipped segments.
func ShellSeparator(source string) bool {
	program, err := syntax.NewParser().Parse(strings.NewReader(source), "")
	return err == nil && len(program.Stmts) == 1 && sourceReadSeparator(program.Stmts[0])
}
