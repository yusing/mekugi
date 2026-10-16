package shellinstrumentation

// Source: internal/vcsguard/display.go:1:96@[465a8ff3]

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"

	"mvdan.cc/sh/v3/syntax"
)

// DisplayScript removes RewriteCommands' exact instrumentation from a display copy.
// The caller identifies session registry directories, including retained ones.
// It does not read the filesystem or evaluate expansions. Unrecognized source
// stays intact, including user assignments, quoted text and heredoc bodies.
func DisplayScript(script string, isGuardDirectory func(string) bool) string {
	if !slices.ContainsFunc(commandGuards, func(guard commandGuardSpec) bool {
		return strings.Contains(script, "/"+guard.directory)
	}) {
		return script
	}
	tree, err := parseScript(script)
	if err != nil {
		return script
	}
	var edits []sourceEdit
	syntax.Walk(tree, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if len(call.Assigns) > 0 {
			assign := call.Assigns[len(call.Assigns)-1]
			guard, bare := commandGuard(call.Args[0])
			if assign.Name != nil && assign.Name.Value == "PATH" && assign.Value != nil && len(assign.Value.Parts) >= 3 && guard != nil && bare {
				// Quote may split a directory containing an apostrophe into
				// several literal parts. The final two parts are : and PATH.
				parts := assign.Value.Parts
				directory, literal := shellsyntax.StaticWord(&syntax.Word{Parts: parts[:len(parts)-2]})
				if literal && filepath.Base(directory) == guard.directory && isGuardDirectory(directory) {
					start, end := int(assign.Pos().Offset()), int(call.Args[0].Pos().Offset())
					item := ""
					if len(call.Assigns) > 1 {
						identity := call.Assigns[len(call.Assigns)-2]
						if identity.Name != nil && identity.Name.Value == ItemEnvironment && identity.Value != nil {
							if value, ok := shellsyntax.StaticWord(identity.Value); ok && value != "" {
								item, start = value, int(identity.Pos().Offset())
							}
						}
					}
					if script[start:end] == itemPathPrefix(directory, item) {
						edits = append(edits, sourceEdit{start: start, end: end})
					}
				}
			}
		}
		i, _ := shellsyntax.ExecutableWord(call.Args)
		if i < 0 || i+3 >= len(call.Args) {
			return true
		}
		helper, static := shellsyntax.StaticWord(call.Args[i])
		if !static || !filepath.IsAbs(helper) || filepath.Base(helper) != "mekugi-exec" {
			return true
		}
		mode, _ := shellsyntax.StaticWord(call.Args[i+1])
		directory, static := shellsyntax.StaticWord(call.Args[i+2])
		if !static || !isGuardDirectory(directory) {
			return true
		}
		target := i + 3
		var sudoDirectory *string
		if flag, _ := shellsyntax.StaticWord(call.Args[target]); flag == SudoDirectoryFlag && target+2 < len(call.Args) {
			value, literal := shellsyntax.StaticWord(call.Args[target+1])
			if !literal || value != "" && !isGuardDirectory(value) {
				return true
			}
			sudoDirectory = new(value)
			target += 2
		}
		item := ""
		if flag, _ := shellsyntax.StaticWord(call.Args[target]); flag == ItemFlag && target+2 < len(call.Args) {
			item, static = shellsyntax.StaticWord(call.Args[target+1])
			if !static || item == "" {
				return true
			}
			target += 2
		}
		word := call.Args[target]
		guard, _ := commandGuard(word)
		if guard == nil || filepath.Base(directory) != guard.directory ||
			(mode != guard.mode && mode != guard.mode+"-default") {
			return true
		}
		start, end := int(call.Args[i].Pos().Offset()), int(word.Pos().Offset())
		prefix := guard.helperPrefix(helper, directory, sudoDirectory, item, mode == guard.mode+"-default")
		if script[start:end] == prefix {
			edits = append(edits, sourceEdit{start: start, end: end})
		}
		return true
	})
	display, err := editSource(script, edits)
	if err != nil {
		return script
	}
	return display
}
