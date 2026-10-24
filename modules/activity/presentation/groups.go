package presentation

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/modules/activity/feed"
)

// Originals returns the canonical flat membership of a displayed value.
func Originals(value Value) []Value {
	if len(value.Members) != 0 {
		return value.Members
	}
	return []Value{value}
}

// Group joins original membership exactly once, even when groups are combined.
// The caller supplies the displayed value and decides disclosure eligibility.
func Group(value Value, members []Value) Value {
	value.Members = nil
	for _, member := range members {
		for _, original := range Originals(member) {
			original.Reads = slices.Clone(original.Reads)
			for i := range original.Reads {
				original.Reads[i].Ranges = slices.Clone(original.Reads[i].Ranges)
				original.Reads[i].RangeLines = slices.Clone(original.Reads[i].RangeLines)
			}
			value.Members = append(value.Members, original)
		}
	}
	return value
}

// TargetRun compacts the first consecutive run of settled target-only operations.
// A negative count keeps an operation's disclosed output on its own row; positive
// counts describe collapsed read content. Nil counts describe source-only values.
func TargetRun(values []Value, counts []int) (Value, int) {
	count := func(i int) int {
		if counts == nil {
			return 0
		}
		return counts[i]
	}
	joins := func(i int) bool {
		b := values[i]
		return b.Kind == "reads" && !b.Running && !b.Skipped && !b.Pending && b.Approval == "" && b.Results == nil && b.ExitCode == 0 &&
			b.Repeated == "" && (b.Verb == "Skill" || b.Verb == "Read" && b.Exited || b.Started.IsZero() && b.Duration == 0) && count(i) >= 0
	}
	end := 1
	for end < len(values) && sameAction(values[0], values[end]) && joins(0) && joins(end) {
		end++
	}
	if end == 1 {
		return values[0], end
	}
	if counts == nil {
		counts = make([]int, end)
	}
	return MergeReads(values[:end], counts[:end]), end
}

// sameAction is shared by target compaction and verb-cell grouping.
func sameAction(a, b Value) bool {
	if a.UserShell || b.UserShell || a.Pending || b.Pending || a.Segment && b.Segment && a.Source == b.Source {
		return false
	}
	if (a.Hook == nil) != (b.Hook == nil) || a.Hook != nil && *a.Hook != *b.Hook {
		return false
	}
	return (a.Kind == "op" || a.Kind == "reads") && (b.Kind == "op" || b.Kind == "reads") &&
		a.Verb != "" && a.Verb == b.Verb && a.Workdir == b.Workdir && a.Owner == b.Owner &&
		a.Running == b.Running && a.Skipped == b.Skipped && a.Pending == b.Pending && a.Effect == b.Effect && a.Exited == b.Exited && a.BatchExit == b.BatchExit &&
		a.Requested == b.Requested && a.Approval == b.Approval
}

// Source: modules/activity/terminalview/blocks.go:369:407@[3e523196].
// MergeReads combines targets and their ranges after disclosure admits adjacent
// reads. counts supplies each collapsed invocation's retained line count.
func MergeReads(values []Value, counts []int) Value {
	value := Group(values[0], values)
	value.Reads, value.Timeouts = nil, nil
	value.Tail, value.TailOmitted = nil, 0
	for i, member := range values {
		for _, timeout := range member.Timeouts {
			if !slices.Contains(value.Timeouts, timeout) {
				value.Timeouts = append(value.Timeouts, timeout)
			}
		}
		for _, read := range member.Reads {
			lines := read.Lines
			if len(member.Reads) == 1 && counts[i] > 0 {
				lines = counts[i]
			}
			rangeLines := make([]int, len(read.Ranges))
			copy(rangeLines, read.RangeLines)
			if len(read.Ranges) == 1 {
				rangeLines[0] = lines
			}
			if at := slices.IndexFunc(value.Reads, func(r feed.Read) bool { return r.Path == read.Path }); at >= 0 {
				value.Reads[at].Ranges = append(value.Reads[at].Ranges, read.Ranges...)
				value.Reads[at].Lines += lines
				value.Reads[at].RangeLines = append(value.Reads[at].RangeLines, rangeLines...)
			} else {
				value.Reads = append(value.Reads, feed.Read{Path: read.Path, Ranges: slices.Clone(read.Ranges), Lines: lines, RangeLines: rangeLines})
			}
		}
		if i > 0 && (value.Verb == "Skill" || value.Verb == "Read") {
			value.Duration += member.Duration
			value.Ended = member.Ended
		}
	}
	return value
}

// Source: modules/activity/terminalview/reasoning.go:53:79@[3e523196].
// Reasoning joins admitted sections while retaining each section's source.
func Reasoning(values []Value) Value {
	value := Group(values[0], values)
	var labels, bodies []string
	for _, section := range values {
		label := section.Label
		if label == "" {
			label = "Thought"
		}
		labels, bodies = append(labels, label), append(bodies, section.Body)
	}
	value.Label, value.Body = strings.Join(labels, " • "), strings.Join(bodies, "\n\n")
	value.Thought = values[len(values)-1].Thought
	return value
}

// Batch is the neutral result of terminal batch disclosure.
func Batch(values []Value) Value { return Group(Value{Kind: "batch"}, values) }

// Source: modules/activity/terminalview/blocks.go:129:177@[3e523196].
// GroupOperations derives shared verb headings, preserving each operation and outcome.
func GroupOperations(blocks []Value) []Value {
	blocks = slices.Clone(blocks)
	previous, previousSource, start := "", "", -1
	var last Value
	lastIndex := -1
	for i := range blocks {
		b := &blocks[i]
		b.GroupCount = 0
		if b.Kind == "filter" {
			// Attached output annotations belong to the preceding operation.
			b.GroupHeader, b.GroupStart = previous, false
			continue
		}
		heading := ""
		if b.EditOperation() {
			heading = b.EditHeading()
		} else if sameAction(last, *b) && !last.EditOperation() {
			heading = b.Verb
			if previous == "" {
				blocks[lastIndex].GroupHeader, blocks[lastIndex].GroupStart = heading, true
				for k := lastIndex + 1; k < i; k++ {
					blocks[k].GroupHeader = heading
				}
				previous = heading
			}
		}
		b.GroupHeader = heading
		b.GroupStart = heading != "" && (heading != previous || b.EditSource != previousSource || b.Workdir != last.Workdir || b.Effect != last.Effect || b.Effect && b.Source != last.Source)
		previous, previousSource = heading, b.EditSource
		if b.GroupStart {
			start = i
		}
		if b.EditOperation() && (b.EditHeader || b.GroupStart || b.Source != last.Source) {
			blocks[start].GroupCount++
		}
		last, lastIndex = *b, i
	}
	return blocks
}

const RequestedEditSuffix = " (requested)"

// editHeading keeps intent and failures from reading as completed edits.
func (b Value) EditHeading() string {
	if b.EditOutcome != "" {
		return "Edit " + b.EditOutcome
	}
	if strings.HasSuffix(b.EditSource, RequestedEditSuffix) {
		return "Edit requested"
	}
	label := b.EditLabel()
	for _, status := range []string{"failed", "declined", "pending", "ran"} {
		if strings.HasSuffix(label, " · "+status) {
			return "Edit " + status
		}
	}
	return "Edited"
}

// editLabel is a file row's label without the source its heading names.
func (b Value) EditLabel() string {
	if b.EditSource == "" {
		return b.Label
	}
	return strings.TrimSuffix(b.Label, " · "+b.EditSource)
}
