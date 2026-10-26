package presentation

import "strings"

// TemporarySource reports a large temporary Create preview that starts folded.
// Original source remains available through explicit disclosure and details.
// Source: modules/activity/terminalview/paint.go:585:587@a90c78603.
func (b Value) TemporarySource() bool {
	return b.Verb == "Create" && strings.HasPrefix(b.EditPath(), "/tmp/") && strings.Count(b.Code, "\n") >= 5
}

// FoldedEditRun counts captured file rows sharing an invocation, group and
// folding policy, and returns the visible prefix length. Create/delete batches
// show ten files; other temporary-file edits retain the ordinary change limit.
// Original evidence remains available through disclosure and details.
// Source: modules/activity/terminalview/blocks.go:79:108@a90c78603.
func FoldedEditRun(values []Value) (count, shown int) {
	limit := func(b Value) int {
		if !(b.EditOperation() && b.GroupHeader == "Edited" &&
			b.ExitCode == 0 && !b.Running && !b.Pending && !b.Skipped && b.Approval == "" && b.Output == 0 &&
			len(b.Tail)+b.TailOmitted == 0 && len(b.Changes) == 0 && b.Code == "" &&
			len(b.Questions) == 0 && !b.DirectoryDeletion()) {
			return 0
		}
		if b.Verb == "Create" || b.Verb == "Delete" {
			return 10
		}
		if strings.HasPrefix(b.EditPath(), "/tmp/") {
			return ChangeRowsShown
		}
		return 0
	}
	if len(values) == 0 {
		return 0, 0
	}
	shown = limit(values[0])
	if shown == 0 {
		return 0, 0
	}
	first, end := values[0], 1
	for end < len(values) && limit(values[end]) == shown && !values[end].GroupStart &&
		values[end].Source == first.Source && values[end].Workdir == first.Workdir {
		end++
	}
	return end, min(end, shown)
}
