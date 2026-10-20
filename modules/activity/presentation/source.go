package presentation

// Source: apps/mekugi/internal/router/subagent_tool_display.go:12:22 and subagent_tool_display_shell.go:23:110,855:860@0a356401.

import (
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
)

// ShellSource interprets unevaluated command source as Activity content. The caller
// supplies its selected directory and removes only authenticated host wrappers.
func ShellSource(script, cwd string) string {
	language := "bash"
	if shellsyntax.IsBatch(script) {
		programs, err := shellsyntax.Split(script)
		if err != nil {
			return "Run\n" + markdownsyntax.SourceFence("", script)
		}
		if len(programs) > 1 {
			displays := make([]string, 0, len(programs))
			for _, program := range programs {
				if display := ShellSource(program, cwd); display != "" {
					displays = append(displays, display)
				}
			}
			return strings.Join(displays, "\n\n")
		}
	}
	parsed, err := shellsyntax.Parse(script)
	if err == nil && len(parsed.Interpreter) == 1 &&
		(parsed.Interpreter[0] == "bash" || parsed.Interpreter[0] == "sh") {
		if summary, ok := ClassifyShell(parsed.Body, cwd); ok {
			return summary
		}
	}
	if strings.TrimSpace(script) == "" {
		return "Run"
	}
	// A multi-line program shows under the command that runs it; a one-line
	// program reads best as the literal command.
	if projection, ok := shellInterpreterScriptProjection(script); ok && projection.Command != "" &&
		strings.Contains(strings.TrimRight(projection.Source, "\n"), "\n") {
		return "Run " + SourceCode(projection.Command) + "\n" + markdownsyntax.SourceFence(projection.Language, projection.Source)
	}
	note := ""
	if shellInterpreterReadsStdin(script) {
		note = " · program read from stdin"
	}
	if err != nil || len(parsed.Interpreter) == 0 {
		language = ""
	} else if parsed.Interpreter[0] != "bash" {
		language = parsed.Interpreter[0]
	}
	language = sourceLanguage(language)
	return "Run" + note + "\n" + markdownsyntax.SourceFence(language, script)
}

func sourceLanguage(interpreter string) string {
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

// SourceCode keeps operation labels literal, using fences for multiline content.
func SourceCode(input string) string {
	if !strings.ContainsAny(input, "\r\n") {
		return markdownsyntax.QuoteCode(input)
	}
	return markdownsyntax.SourceFence("", input+"\n")
}

// RunSource retains an unclassified command when a segment needs a visible row.
func RunSource(source string) string {
	if strings.ContainsAny(source, "\r\n") {
		return ShellSource(source, "")
	}
	return "Run " + SourceCode(source)
}
