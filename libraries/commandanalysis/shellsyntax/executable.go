package shellsyntax

// Source: internal/vcsguard/rewrite.go:174:300@[465a8ff3] command-word classification

import (
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Dynamic marks a shell word whose value is known only at runtime.
const Dynamic = "\x00"

// StaticWord only unquotes literal text. In particular, expand must never see
// parameter, arithmetic, command, process, or pathname expansions here.
func StaticWord(word *syntax.Word) (string, bool) {
	static := true
	syntax.Walk(word, func(node syntax.Node) bool {
		switch node.(type) {
		case nil, *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted:
		default:
			static = false
		}
		return static
	})
	if !static {
		return "", false
	}
	if len(word.Parts) > 0 {
		if lit, ok := word.Parts[0].(*syntax.Lit); ok && strings.HasPrefix(lit.Value, "~") {
			return "", false // Home expansion belongs to the native shell.
		}
	}
	// expand's nil configuration shares mutable scratch state. Keep concurrent
	// hook invocations and callers isolated.
	values, err := expand.Fields(&expand.Config{}, word)
	if err != nil || len(values) != 1 {
		return "", false
	}
	return values[0], true
}

var wrapperOperands = map[string][]string{
	"env":     {"-u", "--unset", "-C", "--chdir", "-a", "--argv0"},
	"command": nil,
	"exec":    {"-a"},
	"nohup":   nil,
	"time":    nil,
	"builtin": nil,
	"timeout": {"-k", "--kill-after", "-s", "--signal"},
	"nice":    {"-n", "--adjustment"},
	"xargs":   {"-a", "--arg-file", "-d", "--delimiter", "-E", "-I", "-L", "-n", "--max-args", "-P", "--max-procs", "-s", "--max-chars"},
}

// ExecutableWord follows wrappers without confusing option operands with the
// command. Lookup-only command invocations stay unchanged.
func ExecutableWord(words []*syntax.Word) (int, bool) {
	values := make([]string, len(words))
	for i, word := range words {
		value, static := StaticWord(word)
		if !static {
			value = wordPrefix(word) + Dynamic
		}
		values[i] = value
	}
	return CommandWord(values)
}

// CommandWord shares wrapper operand handling with embedded-script classification.
func CommandWord(words []string) (int, bool) {
	defaultPath := false
	for i := 0; i < len(words); {
		value := words[i]
		if strings.Contains(value, Dynamic) {
			return i, defaultPath
		}
		wrapper := filepath.Base(value)
		operands, wrapped := wrapperOperands[wrapper]
		if !wrapped {
			return i, defaultPath
		}
		i++
		optionsDone := false
		for i < len(words) {
			arg := words[i]
			if wrapper == "env" && !strings.HasPrefix(arg, "-") && strings.Contains(arg, "=") {
				i++ // The value can expand; it is never evaluated by this parser.
				continue
			}
			if strings.Contains(arg, Dynamic) && wrapper != "timeout" {
				return i, defaultPath
			}
			if !optionsDone && arg == "--" {
				i++
				if wrapper != "env" {
					break
				}
				optionsDone = true
				continue
			}
			if optionsDone || !strings.HasPrefix(arg, "-") || arg == "-" {
				break
			}
			if wrapper == "command" && strings.ContainsAny(arg, "vV") {
				return -1, false
			}
			if wrapper == "command" && strings.Contains(arg, "p") {
				defaultPath = true
			}
			// Options with a separate operand. Attached values remain one word.
			if wrapper == "env" && (arg == "-S" || strings.HasPrefix(arg, "--split-string")) {
				return -1, false
			}
			i++
			if slices.Contains(operands, arg) {
				i++
			}
		}
		if wrapper == "timeout" {
			i++ // Duration.
		}
	}
	return -1, false
}

func wordPrefix(word *syntax.Word) string {
	parts := word.Parts
	for len(parts) > 0 {
		switch part := parts[0].(type) {
		case *syntax.Lit:
			return part.Value
		case *syntax.SglQuoted:
			return part.Value
		case *syntax.DblQuoted:
			parts = part.Parts
		default:
			return ""
		}
	}
	return ""
}
