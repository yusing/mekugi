package presentation

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
)

// Source: apps/mekugi/internal/router/app_server_events.go:763:850@1bcbdd5a.
// HostEditText is one Activity operation per changed file, with its
// line counts taken from the patch Codex applied.
func HostEditText(item observation.Item, cwd string) string {
	var parts []string
	for _, change := range item.Changes {
		added, removed := hostChangeCounts(change)
		verb := "Edit"
		switch change.Kind.Type {
		case "add":
			verb = "Create"
		case "delete":
			verb = "Delete"
		}
		path := pathdisplay.ForWorkspace(cwd, change.Path)
		if moved := change.Kind.MovePath; moved != "" {
			verb, path = "Move", pathdisplay.Move(cwd, change.Path, moved)
		}
		text := verb + " " + markdownsyntax.QuoteCode(path)
		changed := verb != "Edit" || added != 0 || removed != 0
		if changed {
			text += fmt.Sprintf(" · +%d −%d", added, removed)
		}
		switch item.Status {
		case "failed", "declined":
			text += " · " + item.Status
		case "inProgress":
			// Started patches may still await approval; only completion confirms them.
			text += " · pending"
		case "", "completed":
			if !changed {
				// Completion proves execution, not an effect when no diff was supplied.
				text += " · ran"
			}
		}
		parts = append(parts, text+" · apply_patch")
	}
	return strings.Join(parts, "\n\n")
}

// HostEditPages retains host-supplied completed diffs for navigation only.
// It neither captures the filesystem nor creates durable change evidence.
func HostEditPages(item observation.Item, cwd, phase string) []Value {
	if phase != "item/completed" || item.Status != "" && item.Status != "completed" {
		return nil
	}
	var pages []Value
	for _, change := range item.Changes {
		path, diff := pathdisplay.ForWorkspace(cwd, change.Path), change.Diff
		syntaxPath := ""
		if moved := change.Kind.MovePath; moved != "" {
			path = pathdisplay.Move(cwd, change.Path, moved)
			syntaxPath = pathdisplay.ForWorkspace(cwd, moved)
			if body, ok := strings.CutSuffix(diff, "\n\nMoved to: "+moved); ok {
				diff = body + "\n\nMoved to: " + pathdisplay.ForWorkspace(cwd, moved)
			}
			if diff == "" {
				diff = fmt.Sprintf("move %q -> %q\n", pathdisplay.ForWorkspace(cwd, change.Path), pathdisplay.ForWorkspace(cwd, moved))
			}
		}
		if diff == "" {
			continue
		}
		verb, lang := "Edit", "diff"
		switch change.Kind.Type {
		case "add":
			verb, lang = "Create", ""
		case "delete":
			verb, lang = "Delete", ""
		}
		if change.Kind.MovePath != "" {
			verb = "Move"
		}
		pages = append(pages, Value{Kind: "op", Verb: verb, Path: path, SyntaxPath: syntaxPath, Code: diff, Lang: lang, Fenced: true})
	}
	return pages
}

func hostChangeCounts(change observation.FileChange) (added, removed int) {
	lines := strings.Split(strings.TrimSuffix(change.Diff, "\n"), "\n")
	if change.Diff == "" {
		return 0, 0
	}
	switch change.Kind.Type {
	case "add":
		return len(lines), 0
	case "delete":
		return 0, len(lines)
	}
	inHunk := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk && (strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---")):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}
