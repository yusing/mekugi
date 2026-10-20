package presentation

import (
	"slices"
	"strings"
)

var vcsVerbs = []string{"Commit", "Stage", "Diff", "Status", "Check"}

// VCS reports a version-control row: a commit, stage, diff, status or
// whitespace check whose source names git, svn or mchanges.
func (b Value) VCS() bool {
	if b.Kind != "op" || !slices.Contains(vcsVerbs, b.Verb) {
		return false
	}
	_, _, ok := VCSSource(b.Label)
	return ok
}

// VCSSource splits a VCS label into its heading and source.
func VCSSource(label string) (head, source string, ok bool) {
	i := strings.LastIndex(label, "· ")
	if i < 0 || i > 0 && label[i-1] != ' ' {
		return "", "", false
	}
	head, source = strings.TrimSpace(label[:i]), label[i+len("· "):]
	tool, _, _ := strings.Cut(source, " ")
	return head, source, tool == "git" || tool == "svn" || tool == "mchanges"
}

// Source: modules/activity/terminalview/vcs.go:50:73@[3e523196].
// FoldStages joins successful untimed staging to the following commit in the
// same invocation. Original operations remain available for detail and identity.
func FoldStages(blocks []Value) []Value {
	folds := func(i int) bool {
		if b := blocks[i]; b.Verb != "Stage" || !b.VCS() || b.ExitCode != 0 || b.Skipped || !b.Started.IsZero() || b.Duration != 0 {
			return false
		}
		for _, next := range blocks[i+1:] {
			if next.Source != blocks[i].Source {
				return false
			}
			switch {
			case next.Verb == "Stage" && next.VCS() && next.ExitCode == 0 && !next.Skipped:
				continue
			case next.Verb == "Commit" && next.VCS():
				return !next.Skipped
			}
			return false
		}
		return false
	}
	var kept []Value
	var pending []Value
	for i := range blocks {
		if !folds(i) {
			value := blocks[i]
			if len(pending) > 0 && value.Verb == "Commit" && value.VCS() {
				value = Group(value, append(pending, value))
				pending = nil
			}
			kept = append(kept, value)
		} else {
			original := blocks[i]
			original.Subsumed = true
			pending = append(pending, original)
		}
	}
	return kept
}
