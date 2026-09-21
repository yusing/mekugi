package router

import (
	"context"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Display-only syntax boundaries survive transport clipping and never select
// an interpreter for execution. Offsets are bytes in the displayed Input.
type liveDiffSourceSpan struct {
	Offset int
	Path   string
}

// Keep language selection outside the bounded painting window, so scrolling
// past a selector does not silently turn Python or JavaScript back into Bash.
func liveDiffScriptSyntax(input string) []liveDiffSourceSpan {
	var spans []liveDiffSourceSpan
	offset := 0

	for _, program := range shellsyntax.SplitSource(input) {
		path := "stream.sh"
		if parsed, err := shellsyntax.Parse(program); err == nil {
			args := parsed.Interpreter
			if shellsyntax.InterpreterIdentity(args[0]) == "uv" && len(args) >= 3 && args[1] == "run" {
				args = args[2:]
			}
			path = liveDiffLanguagePath(toolActivityLanguage(args[0]))
		}
		spans = append(spans, liveDiffSourceSpan{offset, path})
		offset += len(program)
	}
	return spans
}

func liveDiffLanguagePath(language string) string {
	switch language {
	case "python":
		return "stream.py"
	case "javascript", "typescript":
		return "stream.ts"
	case "ruby":
		return "stream.rb"
	case "perl":
		return "stream.pl"
	case "php":
		return "stream.php"
	case "lua":
		return "stream.lua"
	case "tcl":
		return "stream.tcl"
	case "r":
		return "stream.r"
	case "haskell":
		return "stream.hs"
	case "awk":
		return "stream.awk"
	case "powershell":
		return "stream.ps1"
	case "sql":
		return "stream.sql"
	default:
		return "stream.sh"
	}
}

// Clip offsets together with source, retaining the language of a removed header.
func liveDiffClipSyntax(spans []liveDiffSourceSpan, cut int) []liveDiffSourceSpan {
	var clipped []liveDiffSourceSpan
	for _, span := range spans {
		if span.Offset <= cut {
			clipped = []liveDiffSourceSpan{{Path: span.Path}}
		} else {
			clipped = append(clipped, liveDiffSourceSpan{span.Offset - cut, span.Path})
		}
	}
	return clipped
}

func liveDiffSourceRows(input string, spans []liveDiffSourceSpan) []liveDiffSourceSpan {
	var rows []liveDiffSourceSpan
	offset, index := 0, 0
	for line := range strings.SplitSeq(strings.TrimSuffix(input, "\n"), "\n") {
		for index+1 < len(spans) && spans[index+1].Offset <= offset {
			index++
		}
		rows = append(rows, spans[index])
		offset += len(line) + 1
	}
	return rows
}

func (p *liveDiffPreviewView) colorScript(ctx context.Context, theme liveDiffTheme, start, end int) ([]string, error) {
	var lines []string
	for at := start; at < end; {
		span := p.paths[at]
		next := at
		var source strings.Builder
		for next < end && p.paths[next] == span {
			source.WriteString(livediff.Safe(strings.TrimSuffix(p.source[next].text, "\n"), false))
			source.WriteByte('\n')
			next++
		}
		colored, err := p.renderer.ColorSource(ctx, theme, span.Path, source.String())
		if err != nil {
			return nil, err
		}
		lines = append(lines, colored...)
		at = next
	}
	return lines, nil
}
