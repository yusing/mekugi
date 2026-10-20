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
			}
			value.Members = append(value.Members, original)
		}
	}
	return value
}

// Source: modules/activity/terminalview/blocks.go:369:407@[3e523196].
// MergeReads combines targets and their ranges after disclosure admits adjacent
// reads. counts supplies each collapsed invocation's retained line count.
func MergeReads(values []Value, counts []int) Value {
	value := Group(values[0], values)
	value.Reads = nil
	value.Tail, value.TailOmitted = nil, 0
	for i, member := range values {
		for _, read := range member.Reads {
			lines := read.Lines
			if len(member.Reads) == 1 && counts[i] > 0 {
				lines = counts[i]
			}
			if at := slices.IndexFunc(value.Reads, func(r feed.Read) bool { return r.Path == read.Path }); at >= 0 {
				value.Reads[at].Ranges = append(value.Reads[at].Ranges, read.Ranges...)
				value.Reads[at].Lines += lines
			} else {
				value.Reads = append(value.Reads, feed.Read{Path: read.Path, Ranges: slices.Clone(read.Ranges), Lines: lines})
			}
		}
		if i > 0 && value.Verb == "Skill" {
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
// GroupOperations derives shared edit headings and invocation counts.
func GroupOperations(blocks []Value) []Value {
	blocks = slices.Clone(blocks)
	previous, previousSource, start := "", "", -1
	for i := range blocks {
		b := &blocks[i]
		b.GroupCount = 0
		if b.Kind == "filter" {
			// Attached output annotations belong to the preceding operation.
			b.GroupHeader, b.GroupStart = previous, false
			continue
		}
		heading := ""
		if b.EditSource != "" {
			heading = b.EditHeading()
		}
		b.GroupHeader = heading
		b.GroupStart = heading != "" && (heading != previous || b.EditSource != previousSource)
		previous, previousSource = heading, b.EditSource
		if b.GroupStart {
			start = i
		}
		if b.EditSource != "" && (b.EditHeader || b.GroupStart) {
			blocks[start].GroupCount++
		}
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
	for _, status := range []string{"failed", "declined", "pending"} {
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
