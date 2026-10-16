package shellinstrumentation

// Source: internal/vcsguard/rewrite.go:1:173@[465a8ff3] guard rewriting

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// RewriteCommands instruments executable words, not statements. The selected native
// shell still expands arguments and owns control flow, redirections and status.
// Parsing never executes expansions or reads scripts from the filesystem.
// It retains the host item ID and covers sudo independently of remote-write guarding.
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
		i, defaultPath := shellsyntax.ExecutableWord(call.Args)
		if i < 0 {
			return true
		}
		word := call.Args[i]
		guard, bare := commandGuard(word)
		if guard == nil {
			return true
		}
		commandDirectory := directory
		if guard.directory == SudoDirectory || guard.mode == "--vcs-shell" && commandDirectory == "" {
			commandDirectory = sudoDirectory
		}
		if commandDirectory == "" {
			return true
		}
		start, end := int(word.Pos().Offset()), int(word.End().Offset())
		if i == 0 && bare {
			// Preserve aliases and shell functions. Only external lookup needs
			// our PATH entry, after any command-local PATH assignment.
			prefix := itemPathPrefix(commandDirectory, item)
			if !strings.HasSuffix(script[int(call.Pos().Offset()):start], prefix) {
				edits = append(edits, sourceEdit{start, start, prefix})
			}
		} else {
			prefix := guard.helperPrefix(helper, commandDirectory, &sudoDirectory, item, defaultPath)
			edits = append(edits, sourceEdit{start, end, prefix + script[start:end]})
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

// SudoDirectoryFlag carries the second guard resource through clean environments.
const SudoDirectoryFlag = "--sudo-directory"

var Shells = []string{"sh", "bash", "dash", "ksh"}

// commandGuards owns executable recognition, runtime directory names and helper
// modes for both instrumentation and its display projection.
var commandGuards = []commandGuardSpec{
	{VCSDirectory, "--vcs-command", commandanalysis.VCSTools, true},
	{VCSDirectory, "--vcs-shell", Shells, false},
	{SudoDirectory, "--sudo-command", []string{"sudo"}, true},
}

type commandGuardSpec struct {
	directory, mode string
	tools           []string
	dynamic         bool
}

func commandGuard(word *syntax.Word) (*commandGuardSpec, bool) {
	value, static := shellsyntax.StaticWord(word)
	name := filepath.Base(value)
	for i := range commandGuards {
		guard := &commandGuards[i]
		for _, tool := range guard.tools {
			if static && name == tool || !static && guard.dynamic && toolPathSuffix(word, tool) {
				return guard, static && value == name
			}
		}
	}
	return nil, false
}

// IsGuardDirectory recognizes an instrumentation directory name. Callers must
// separately validate its session registry or exact invocation-local path.
func IsGuardDirectory(directory string) bool {
	for _, guard := range commandGuards {
		if filepath.Base(directory) == guard.directory {
			return true
		}
	}
	return false
}

func (g *commandGuardSpec) helperPrefix(helper, directory string, sudoDirectory *string, item string, defaultPath bool) string {
	mode := g.mode
	if defaultPath {
		mode += "-default"
	}
	prefix := shellsyntax.Quote(helper) + " " + mode + " " + shellsyntax.Quote(directory) + " "
	if g.mode == "--vcs-shell" && sudoDirectory != nil {
		prefix += SudoDirectoryFlag + " " + shellsyntax.Quote(*sudoDirectory) + " "
	}
	if item != "" {
		prefix += ItemFlag + " " + shellsyntax.Quote(item) + " "
	}
	return prefix
}

// toolPathSuffix recognizes a dynamic directory while leaving its expansion
// to the native shell, for example "$tools/git" or ${tools}/git.
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
	return ok && strings.Contains(last.Value, "/") && filepath.Base(last.Value) == name
}

// acceptsDirectory keeps shell trampolines independent of either guard policy.
func (g commandGuardSpec) acceptsDirectory(directory string) bool {
	name := filepath.Base(directory)
	return name == g.directory || g.mode == "--vcs-shell" && name == SudoDirectory
}
