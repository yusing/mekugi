package feed

// Source: internal/ui/activity/output.go@[11705e68]

import (
	"slices"
	"strings"
)

// Output keeps a command's host output for reading in full, apart from the
// display tail rows show: its latest OutputBytes, sanitized line by line, and
// a count of the lines before them. A Retention bounds what a session keeps
// across commands. A serialized session owner reads and mutates outputs.
type Output struct {
	buffer    LineBuffer
	lines     []string
	bytes     int  // Retained line bytes, charged to the retention.
	dropped   int  // Lines before lines.
	truncated bool // At least one completed line exceeded OutputLineBytes.
	released  bool
	done      bool
	exited    bool // The host reported an exit status.
	exit      int
	version   int
	snapshot  *OutputView
	owner     *Retention
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
		o.lines, o.buffer.Line, o.bytes, o.released = nil, nil, 0, true
		o.changed()
		r.outputs = slices.Delete(r.outputs, i, i+1)
	}
}

// Write appends streamed output. A carriage return starts its line over, as
// a terminal redraws a progress line.
func (o *Output) Write(output string) {
	if o.done || o.released {
		return
	}
	before := o.bytes + len(o.buffer.Line)
	o.buffer.Write(output, OutputLineBytes, o.endLine)
	o.trim()
	o.changed()
	if o.owner != nil {
		o.owner.charge(o.bytes + len(o.buffer.Line) - before)
	}
}

func (o *Output) endLine() {
	if len(o.buffer.Line) > OutputLineBytes {
		o.truncated = true
	}
	line := LineText(o.buffer.Line, OutputLineBytes)
	o.lines = append(o.lines, line)
	o.bytes += len(line) + outputLineOverhead
	o.buffer.Line = o.buffer.Line[:0]
	o.trim()
}

// trim drops complete head lines, clearing their references as they leave.
func (o *Output) trim() {
	for o.bytes+len(o.buffer.Line) > OutputBytes && len(o.lines) > 0 {
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
		before := o.bytes + len(o.buffer.Line)
		o.lines, o.buffer, o.bytes, o.dropped = nil, LineBuffer{}, 0, 0
		o.truncated = false
		if o.owner != nil {
			o.owner.bytes -= before
		}
		o.Write(*aggregate)
	}
	if len(o.buffer.Line) > 0 {
		before := o.bytes + len(o.buffer.Line)
		o.endLine()
		if o.owner != nil {
			o.owner.charge(o.bytes - before)
		}
	}
	o.done = true
	if exit != nil {
		o.exited, o.exit = true, *exit
	}
	o.changed()
	if o.owner != nil {
		o.owner.charge(0) // Settled output may now be released.
	}
}

// Release ends output retention when segment attribution becomes unavailable.
// The session owner serializes it with Write, Finish and View.
func (o *Output) Release() {
	before := o.bytes + len(o.buffer.Line)
	o.lines, o.buffer.Line, o.bytes = nil, nil, 0
	o.done, o.released = true, true
	o.changed()
	if o.owner != nil {
		o.owner.charge(-before)
		o.owner = nil
	}
}

// OutputView is a read-only snapshot. Its lines are independent of later output
// mutations; consumers must not modify the published slice.
type OutputView struct {
	Lines     []string // Retained lines, then an unfinished one.
	Dropped   int      // Lines before Lines no longer retained.
	Truncated bool     // The current stream or replacement aggregate lost bytes to the line cap.
	Released  bool     // The session released this output for newer output.
	Done      bool
	Exited    bool // Exit is the host's reported status.
	Exit      int
}

// Version changes whenever the output's view does.
func (o *Output) Version() int {
	return o.version
}

// HasContent checks for nonblank retained text without copying the full stream.
func (o *Output) HasContent() bool {
	for _, line := range o.lines {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return len(o.buffer.Line) > 0 && strings.TrimSpace(LineText(o.buffer.Line, OutputLineBytes)) != ""
}

func (o *Output) changed() {
	o.version++
	o.snapshot = nil
}

func (o *Output) View() OutputView {
	if o.snapshot != nil {
		return *o.snapshot
	}
	lines := slices.Clone(o.lines)
	if len(o.buffer.Line) > 0 {
		lines = append(lines, LineText(o.buffer.Line, OutputLineBytes))
	}
	// Trailing blank lines only pad the output.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	view := o.viewState()
	view.Lines = lines
	o.snapshot = &view
	return *o.snapshot
}

// CompleteText returns complete retained logical rows, including trailing blank
// rows omitted from display snapshots. Rows remain normalized and each ends in
// a newline; the original presence of a final terminator is not retained.
// Incomplete, truncated, dropped or released output supplies no complete text.
func (o *Output) CompleteText() (string, bool) {
	if !o.done || o.truncated || o.dropped > 0 || o.released {
		return "", false
	}
	if len(o.lines) == 0 {
		return "", true
	}
	return strings.Join(o.lines, "\n") + "\n", true
}

// Context returns a bounded, immutable prefix for feed syntax detection without
// populating the full snapshot cache. The budget includes line slots, as retained
// output accounting does. limited distinguishes a bounded prefix from complete
// retained evidence; it does not claim that the host truncated its output.
func (o *Output) Context(budget int) (view OutputView, limited bool) {
	view = o.viewState()
	count, size := 0, 0
	for _, line := range o.lines {
		cost := len(line) + outputLineOverhead
		if size+cost > budget {
			limited = true
			break
		}
		size += cost
		count++
	}
	view.Lines = slices.Clone(o.lines[:count])
	if !limited && len(o.buffer.Line) > 0 {
		line := LineText(o.buffer.Line, OutputLineBytes)
		if size+len(line)+outputLineOverhead > budget {
			limited = true
		} else {
			view.Lines = append(view.Lines, line)
		}
	}
	if !limited {
		for len(view.Lines) > 0 && strings.TrimSpace(view.Lines[len(view.Lines)-1]) == "" {
			view.Lines = view.Lines[:len(view.Lines)-1]
		}
	}
	return view, limited
}

func (o *Output) viewState() OutputView {
	return OutputView{Dropped: o.dropped, Truncated: o.truncated || len(o.buffer.Line) > OutputLineBytes, Released: o.released, Done: o.done, Exited: o.exited, Exit: o.exit}
}
