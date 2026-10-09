package vcsguard

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Rewrite instruments executable words, not statements. The selected native
// shell still expands arguments and owns control flow, redirections and status.
// Parsing never executes expansions or reads scripts from the filesystem.
func Rewrite(script, helper, directory string) (string, error) {
	return RewriteForItem(script, helper, directory, "")
}

// RewriteForItem retains the host item ID in command-local guard input.
func RewriteForItem(script, helper, directory, item string) (string, error) {
	return RewriteCommands(script, helper, directory, "", item)
}

// RewriteCommands also covers sudo independently of remote-write guarding.
func RewriteCommands(script, helper, directory, sudoDirectory, item string) (string, error) {
	tree, err := parseScript(script)
	if err != nil {
		return "", fmt.Errorf("VCS guard: cannot parse command: %w", err)
	}
	var edits []sourceEdit
	syntax.Walk(tree, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		i, defaultPath := executableWord(call.Args)
		if i < 0 {
			return true
		}
		word := call.Args[i]
		value, static := staticWord(word)
		name := filepath.Base(value)
		tool := directory != "" && ((static && IsTool(name)) || (!static && vcsPathSuffix(word)))
		sudo := sudoDirectory != "" && ((static && name == "sudo") || (!static && toolPathSuffix(word, "sudo")))
		shell := directory != "" && static && slices.Contains(Shells, name)
		if tool || shell || sudo {
			commandDirectory := directory
			if sudo {
				commandDirectory = sudoDirectory
			}
			start, end := int(word.Pos().Offset()), int(word.End().Offset())
			if i == 0 && static && value == name {
				// Preserve aliases and shell functions. Only external lookup needs
				// our PATH entry, after any command-local PATH assignment.
				prefix := itemPathPrefix(commandDirectory, item)
				if !strings.HasSuffix(script[int(call.Pos().Offset()):start], prefix) {
					edits = append(edits, sourceEdit{start, start, prefix})
				}
			} else {
				mode := "--vcs-command"
				if sudo {
					mode = "--sudo-command"
				}
				if shell {
					mode = "--vcs-shell"
				}
				if defaultPath {
					mode += "-default"
				}
				prefix := shellsyntax.Quote(helper) + " " + mode + " " + shellsyntax.Quote(commandDirectory) + " "
				if item != "" {
					prefix += ItemFlag + " " + shellsyntax.Quote(item) + " "
				}
				edits = append(edits, sourceEdit{start, end, prefix + script[start:end]})
			}
		}
		return true
	})
	return editSource(script, edits)
}

func pathPrefix(directory string) string {
	return "PATH=" + shellsyntax.Quote(directory) + ":\"${PATH-/bin:/usr/bin}\" "
}

func itemPathPrefix(directory, item string) string {
	if item == "" {
		return pathPrefix(directory)
	}
	return ItemEnvironment + "=" + shellsyntax.Quote(item) + " " + pathPrefix(directory)
}

func parseScript(script string) (*syntax.File, error) {
	// Keep authored bytes; instrumentation does not print a different dialect.
	return syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
}

func editSource(script string, edits []sourceEdit) (string, error) {
	if len(edits) == 0 {
		return script, nil
	}
	slices.SortFunc(edits, func(a, b sourceEdit) int { return a.start - b.start })
	var result strings.Builder
	pos := 0
	for _, edit := range edits {
		if edit.start < pos {
			return "", fmt.Errorf("VCS guard: overlapping command expansions")
		}
		result.WriteString(script[pos:edit.start])
		result.WriteString(edit.text)
		pos = edit.end
	}
	result.WriteString(script[pos:])
	return result.String(), nil
}

type sourceEdit struct {
	start, end int
	text       string
}

var Shells = []string{"sh", "bash", "dash", "ksh"}

// vcsPathSuffix recognizes a dynamic directory while leaving its expansion
// to the native shell, for example "$tools/git" or ${tools}/git.
func vcsPathSuffix(word *syntax.Word) bool { return toolPathSuffix(word, "") }

func toolPathSuffix(word *syntax.Word, name string) bool {
	parts := word.Parts
	if len(parts) == 0 {
		return false
	}
	if quoted, ok := parts[len(parts)-1].(*syntax.DblQuoted); ok {
		parts = quoted.Parts
	}
	if len(parts) == 0 {
		return false
	}
	last, ok := parts[len(parts)-1].(*syntax.Lit)
	return ok && strings.Contains(last.Value, "/") && ((name == "" && IsTool(last.Value)) || (name != "" && filepath.Base(last.Value) == name))
}

// staticWord only unquotes literal text. In particular, expand must never see
// parameter, arithmetic, command, process, or pathname expansions here.
func staticWord(word *syntax.Word) (string, bool) {
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

// executableWord follows wrappers without confusing option operands with the
// command. Lookup-only command invocations stay unchanged.
func executableWord(words []*syntax.Word) (int, bool) {
	values := make([]string, len(words))
	for i, word := range words {
		value, static := staticWord(word)
		if !static {
			value = wordPrefix(word) + dynamic
		}
		values[i] = value
	}
	return commandWord(values)
}

// commandWord shares wrapper operand handling with embedded-script classification.
func commandWord(words []string) (int, bool) {
	defaultPath := false
	for i := 0; i < len(words); {
		value := words[i]
		if strings.Contains(value, dynamic) {
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
			if strings.Contains(arg, dynamic) && wrapper != "timeout" {
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
