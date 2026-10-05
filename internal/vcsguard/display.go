package vcsguard

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// DisplayScript removes Rewrite's exact instrumentation from a display copy.
// The caller identifies session registry directories, including retained ones.
// It does not read the filesystem or evaluate expansions. Unrecognized source
// stays intact, including user assignments, quoted text and heredoc bodies.
func DisplayScript(script string, isGuardDirectory func(string) bool) string {
	if !strings.Contains(script, "/"+Directory) {
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
			value, static := staticWord(call.Args[0])
			if assign.Name != nil && assign.Name.Value == "PATH" && assign.Value != nil && len(assign.Value.Parts) >= 3 && static &&
				(slices.Contains(Tools, value) || slices.Contains(Shells, value)) {
				// Quote may split a directory containing an apostrophe into
				// several literal parts. The final two parts are : and PATH.
				parts := assign.Value.Parts
				directory, literal := staticWord(&syntax.Word{Parts: parts[:len(parts)-2]})
				if literal && isGuardDirectory(directory) {
					start, end := int(assign.Pos().Offset()), int(call.Args[0].Pos().Offset())
					if script[start:end] == pathPrefix(directory) {
						edits = append(edits, sourceEdit{start: start, end: end})
					}
				}
			}
		}
		i, _ := executableWord(call.Args)
		if i < 0 || i+3 >= len(call.Args) {
			return true
		}
		helper, static := staticWord(call.Args[i])
		if !static || !filepath.IsAbs(helper) || filepath.Base(helper) != "mekugi-exec" {
			return true
		}
		mode, _ := staticWord(call.Args[i+1])
		directory, static := staticWord(call.Args[i+2])
		if !static || !isGuardDirectory(directory) {
			return true
		}
		word := call.Args[i+3]
		value, static := staticWord(word)
		tool := static && slices.Contains(Tools, filepath.Base(value)) || !static && vcsPathSuffix(word)
		shell := static && slices.Contains(Shells, filepath.Base(value))
		if !(tool && (mode == "--vcs-command" || mode == "--vcs-command-default") ||
			shell && (mode == "--vcs-shell" || mode == "--vcs-shell-default")) {
			return true
		}
		start, end := int(call.Args[i].Pos().Offset()), int(word.Pos().Offset())
		prefix := shellsyntax.Quote(helper) + " " + mode + " " + shellsyntax.Quote(directory) + " "
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
