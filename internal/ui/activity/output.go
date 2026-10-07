package activity

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
)

// Output keeps a command's host output for reading in full, apart from the
// display tail rows show: its latest OutputBytes, sanitized line by line, and
// a count of the lines before them. A Retention bounds what a session keeps
// across commands. Outputs belong to the UI goroutine.
type Output struct {
	outputLineBuffer
	lines         []string
	bytes         int    // Retained line bytes, charged to the retention.
	dropped       int    // Lines before lines.
	truncated     bool   // At least one completed line exceeded OutputLineBytes.
	prefixOmitted bool   // The host supplies only a tail with an unknown number of earlier lines.
	reference     string // Managed full-output evidence, apart from display retention.
	released      bool
	done          bool
	exited        bool // The host reported an exit status.
	exit          int
	version       int
	owner         *Retention
}

// Host aggregates stop at 1 MiB; a live stream keeps as much of its latest
// output. A session keeps OutputRetainedBytes across commands.
const (
	OutputBytes         = 1 << 20
	OutputLineBytes     = 16 << 10
	outputLineOverhead  = 16 // A retained string slot, including empty lines.
	OutputRetainedBytes = 16 << 20
)

// Retention bounds the output a session keeps, releasing the oldest settled
// output first. Output still running is never released.
type Retention struct {
	outputs []*Output
	bytes   int
}

// New starts retaining a command's output.
func (r *Retention) New() *Output {
	o := &Output{owner: r}
	// Settled empty output has nothing to release.
	r.outputs = append(slices.DeleteFunc(r.outputs, func(o *Output) bool { return o.done && o.bytes == 0 }), o)
	return o
}

func (r *Retention) charge(n int) {
	r.bytes += n
	for i := 0; r.bytes > OutputRetainedBytes && i < len(r.outputs); {
		o := r.outputs[i]
		if !o.done {
			i++
			continue
		}
		r.bytes -= o.bytes
		o.lines, o.line, o.bytes, o.released = nil, nil, 0, true
		o.version++
		r.outputs = slices.Delete(r.outputs, i, i+1)
	}
}

// Write appends streamed output. A carriage return starts its line over, as
// a terminal redraws a progress line.
func (o *Output) Write(output string) {
	if o.done || o.released {
		return
	}
	before := o.bytes + len(o.line)
	o.outputLineBuffer.write(output, OutputLineBytes, o.endLine)
	o.trim()
	o.version++
	if o.owner != nil {
		o.owner.charge(o.bytes + len(o.line) - before)
	}
}

// Snapshot replaces the current tail without inventing deltas or line counts.
// Its Output identity remains stable for already-open dialogs.
func (o *Output) Snapshot(output string, prefixOmitted bool) {
	if o.done || o.released {
		return
	}
	before := o.bytes + len(o.line)
	o.lines, o.line, o.cr, o.bytes, o.dropped = nil, nil, false, 0, 0
	o.truncated, o.prefixOmitted = false, prefixOmitted
	if o.owner != nil {
		o.owner.bytes -= before
	}
	o.Write(output)
}

func (o *Output) endLine() {
	line := string(o.line)
	if len(line) > OutputLineBytes {
		o.truncated = true
		line = strings.ToValidUTF8(line[:OutputLineBytes], "") + "…"
	}
	line = strings.TrimRight(livediff.Safe(line, false), " ")
	o.lines = append(o.lines, line)
	o.bytes += len(line) + outputLineOverhead
	o.line = o.line[:0]
	o.trim()
}

// trim drops complete head lines, clearing their references as they leave.
func (o *Output) trim() {
	for o.bytes+len(o.line) > OutputBytes && len(o.lines) > 0 {
		o.bytes -= len(o.lines[0]) + outputLineOverhead
		o.lines[0] = ""
		o.lines = o.lines[1:]
		o.dropped++
	}
}

// Finish replaces streamed output with the host's aggregate, when it reports
// one, and records the command's exit, when known. Later output is ignored.
func (o *Output) Finish(aggregate *string, exit *int) {
	if o.done {
		return
	}
	if aggregate != nil && !o.released {
		o.Snapshot(*aggregate, false)
	}
	if len(o.line) > 0 {
		before := o.bytes + len(o.line)
		o.endLine()
		if o.owner != nil {
			o.owner.charge(o.bytes - before)
		}
	}
	o.done = true
	if exit != nil {
		o.exited, o.exit = true, *exit
	}
	o.version++
	if o.owner != nil {
		o.owner.charge(0) // Settled output may now be released.
	}
}

// Reconcile replaces a settled tail when the host later supplies its complete
// terminal aggregate. Open dialogs keep the same output identity. Ordinary
// streaming still ignores settled outputs through Write and Snapshot.
func (o *Output) Reconcile(aggregate string) {
	if o.released {
		return
	}
	o.done = false
	o.Finish(&aggregate, nil)
}

// RetainReference keeps omitted evidence accessible from this command's dialog.
func (o *Output) RetainReference(reference string) {
	if o.reference != reference {
		o.reference = reference
		o.version++
	}
}

// Release ends output retention when segment attribution becomes unavailable.
// It belongs to the UI goroutine, like Write and Finish.
func (o *Output) Release() {
	before := o.bytes + len(o.line)
	o.lines, o.line, o.bytes = nil, nil, 0
	o.done, o.released = true, true
	o.version++
	if o.owner != nil {
		o.owner.charge(-before)
	}
}

// OutputView is what an Output retains at one moment.
type OutputView struct {
	Lines         []string // Retained lines, then an unfinished one.
	Dropped       int      // Lines before Lines no longer retained.
	Truncated     bool     // A retained line lost bytes to the per-line limit.
	Released      bool     // The session released this output for newer output.
	Done          bool
	Exited        bool // Exit is the host's reported status.
	Exit          int
	PrefixOmitted bool // Earlier host bytes are absent; their line count is unknown.
	Reference     string
}

// Version changes whenever the output's view does.
func (o *Output) Version() int {
	return o.version
}

func (o *Output) View() OutputView {
	lines := o.lines
	if len(o.line) > 0 {
		line := string(o.line)
		if len(line) > OutputLineBytes {
			line = strings.ToValidUTF8(line[:OutputLineBytes], "") + "…"
		}
		line = strings.TrimRight(livediff.Safe(line, false), " ")
		lines = append(slices.Clip(lines), line)
	}
	// Trailing blank lines only pad the output.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return OutputView{Lines: lines, Dropped: o.dropped, Truncated: o.truncated || len(o.line) > OutputLineBytes, Released: o.released, Done: o.done, Exited: o.exited, Exit: o.exit, PrefixOmitted: o.prefixOmitted, Reference: o.reference}
}
