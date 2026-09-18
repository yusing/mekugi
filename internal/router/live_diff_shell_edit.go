package router

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/internal/hpatchsyntax"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Decode only literal standalone edit input. No interpreter, expansion callbacks,
// filesystem input redirections, or recovery state participate in previews.
func liveDiffShellEdit(input, directory string) (string, string, bool) {
	header, err := shellsyntax.Parse(input)
	if err != nil || len(header.Interpreter) != 1 {
		return "", "", false
	}
	variant := syntax.LangBash
	switch shellInterpreterName(header.Interpreter[0]) {
	case "bash":
	case "sh":
		variant = syntax.LangPOSIX
	default:
		return "", "", false
	}
	if value, exists := header.Params["workdir"]; exists {
		workdir, ok := value.(string)
		if !ok || !filepath.IsAbs(workdir) {
			return "", "", false
		}
		directory = workdir
	}
	body := header.Body
	parse := func(source string) (*syntax.File, error) {
		return syntax.NewParser(syntax.Variant(variant)).Parse(strings.NewReader(source), "")
	}
	program, err := parse(body)
	partialLine := false
	if err != nil {
		// An incomplete-shell diagnostic points at the actual redirect token,
		// including after comments or blank lines. Share delimiter decoding,
		// then validate the entire completed shell AST before projection.
		frame := liveDiffIncompleteHeredoc(body, err, variant)
		lines := hpatchsyntax.SplitPhysicalLines(body)
		if frame.Delimiter != "" {
			last := lines[len(lines)-1]
			candidate := last.Text
			if frame.StripTabs {
				candidate = strings.TrimLeft(candidate, "\t")
			}
			if last.Terminator == "" && candidate != "" && strings.HasPrefix(frame.Delimiter, candidate) {
				body = strings.TrimSuffix(body, last.Text)
			}
			partialLine = !strings.HasSuffix(body, "\n")
			if partialLine {
				body += "\n"
			}
			program, err = parse(body + frame.Delimiter + "\n")
		} else {
			// Recover only a missing final quote, then validate normally.
			recovered, _ := syntax.NewParser(syntax.Variant(variant), syntax.RecoverErrors(1)).Parse(strings.NewReader(body), "")
			if recovered != nil && len(recovered.Stmts) == 1 {
				if call, ok := recovered.Stmts[0].Cmd.(*syntax.CallExpr); ok && len(call.Args) == 2 {
					parts := call.Args[1].Parts
					if len(parts) > 0 {
						switch quote := parts[len(parts)-1].(type) {
						case *syntax.SglQuoted:
							if quote.Right.IsRecovered() {
								program, err = parse(body + "'")
							}
						case *syntax.DblQuoted:
							if quote.Right.IsRecovered() {
								program, err = parse(body + `"`)
							}
						}
					}
				}
			}
		}
	}
	if err != nil || program == nil || len(program.Stmts) != 1 {
		return "", "", false
	}
	stmt := program.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Disown || stmt.Negated || len(call.Assigns) != 0 ||
		len(call.Args) == 0 || call.Args[0].Lit() != "hpatch" {
		return "", "", false
	}
	if len(call.Args) == 2 && len(stmt.Redirs) == 0 {
		script, literal := shellCatLiteral(call.Args[1])
		return script, directory, literal
	}
	if len(call.Args) != 1 || len(stmt.Redirs) != 1 {
		return "", "", false
	}
	redirect := stmt.Redirs[0]
	if (redirect.Op != syntax.Hdoc && redirect.Op != syntax.DashHdoc) ||
		(redirect.N != nil && redirect.N.Value != "0") || redirect.Hdoc == nil {
		return "", "", false
	}
	var source strings.Builder
	for _, part := range redirect.Hdoc.Parts {
		literal, ok := part.(*syntax.Lit)
		if !ok {
			return "", "", false
		}
		source.WriteString(literal.Value)
	}
	script := source.String()
	// A quoted delimiter suppresses all heredoc expansion. For an unquoted
	// delimiter, only literal escape processing is safe; AST validation above
	// has excluded parameters and command substitutions.
	quoted := false
	for _, part := range redirect.Word.Parts {
		switch part := part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			quoted = true
		case *syntax.Lit:
			quoted = quoted || strings.Contains(part.Value, `\`)
		}
	}
	if !quoted {
		script, err = expand.Document(&expand.Config{}, redirect.Hdoc)
		if err != nil {
			return "", "", false
		}
	}
	if redirect.Op == syntax.DashHdoc {
		lines := strings.Split(script, "\n")
		for i := range lines {
			lines[i] = strings.TrimLeft(lines[i], "\t")
		}
		script = strings.Join(lines, "\n")
	}
	if partialLine {
		script = strings.TrimSuffix(script, "\n")
	}
	return script, directory, true
}

// Use the shell parser's error position, not a scan for shell-looking text in
// quoted arguments or heredoc bodies. Only the delimiter word is HPATCH-shared.
func liveDiffIncompleteHeredoc(body string, parseErr error, variant syntax.LangVariant) hpatchsyntax.CommandFrame {
	diagnostic, ok := errors.AsType[syntax.ParseError](parseErr)
	if !ok {
		return hpatchsyntax.CommandFrame{}
	}
	offset := int(diagnostic.Pos.Offset())
	if offset >= len(body) || !strings.HasPrefix(body[offset:], "<<") {
		return hpatchsyntax.CommandFrame{}
	}
	marker, _, newline := strings.Cut(body[offset:], "\n")
	if !newline {
		return hpatchsyntax.CommandFrame{}
	}
	wordSource := strings.TrimPrefix(marker[2:], "-")
	wordOffset := len(marker) - len(wordSource)
	for word, err := range syntax.NewParser(syntax.Variant(variant)).WordsSeq(strings.NewReader(wordSource)) {
		if err != nil {
			break
		}
		header := "shell " + marker[:wordOffset+int(word.End().Offset())]
		frame, _ := hpatchsyntax.FrameCommand([]hpatchsyntax.PhysicalLine{{Text: header, Terminator: "\n"}}, 0, header)
		return frame
	}
	return hpatchsyntax.CommandFrame{}
}
