package presentation

import "strings"

// TemporarySource reports a large temporary Create preview that starts folded.
// Original source remains available through explicit disclosure and details.
// Source: modules/activity/terminalview/paint.go:585:587@a90c78603.
func (b Value) TemporarySource() bool {
	return b.Verb == "Create" && strings.HasPrefix(b.EditPath(), "/tmp/") && strings.Count(b.Code, "\n") >= 5
}

// TemporaryEditRun counts the leading captured temporary-file rows that share
// an invocation and group. A run's first ChangeRowsShown files stay visible;
// subsequent files may fold without discarding their original evidence.
// Source: modules/activity/terminalview/blocks.go:79:108@a90c78603.
func TemporaryEditRun(values []Value) int {
	temporary := func(b Value) bool {
		return b.EditOperation() && b.GroupHeader == "Edited" &&
			b.ExitCode == 0 && !b.Running && !b.Pending && !b.Skipped && b.Approval == "" && b.Output == 0 &&
			len(b.Tail)+b.TailOmitted == 0 && len(b.Changes) == 0 && b.Code == "" &&
			len(b.Questions) == 0 && !b.DirectoryDeletion() && strings.HasPrefix(b.EditPath(), "/tmp/")
	}
	if len(values) == 0 || !temporary(values[0]) {
		return 0
	}
	first, end := values[0], 1
	for end < len(values) && temporary(values[end]) && !values[end].GroupStart &&
		values[end].Source == first.Source && values[end].Workdir == first.Workdir {
		end++
	}
	return end
}
