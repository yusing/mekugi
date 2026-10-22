package appserver

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/libraries/textsanitize"
)

// Write retains bounded lines independently of child read chunk boundaries.
// Repetitions consume a count rather than evicting unrelated diagnostics.
func (d *Diagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		line, rest, complete := bytes.Cut(p, []byte{'\n'})
		if len(line) > 8<<10 {
			line = line[len(line)-(8<<10):]
			d.partial = nil
			d.omitted = true
		}
		d.partial = append(d.partial, line...)
		if len(d.partial) > 8<<10 {
			d.partial = bytes.Clone(d.partial[len(d.partial)-(8<<10):])
			d.omitted = true
		}
		if complete {
			text := textsanitize.Safe(string(d.partial), false)
			d.partial = nil
			found := false
			for i := range d.lines {
				if d.lines[i].text == text {
					d.lines[i].count++
					found = true
					break
				}
			}
			if !found && strings.TrimSpace(text) != "" {
				for len(d.lines) > 0 && (len(d.lines) >= 256 || d.bytes+len(text) > 48<<10) {
					d.bytes -= len(d.lines[0].text)
					d.lines[0] = diagnosticLine{}
					d.lines = d.lines[1:]
					d.omitted = true
				}
				d.lines = append(d.lines, diagnosticLine{text: text, count: 1})
				d.bytes += len(text)
			}
		}
		p = rest
	}
	return n, nil
}

// Snapshot is detached and safe to print after terminal restoration. It does not
// consume the tail: independently attached clients may quit at different times.
func (d *Diagnostics) Snapshot() string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out strings.Builder
	if d.omitted {
		out.WriteString("[earlier diagnostics omitted]\n")
	}
	for _, line := range d.lines {
		out.WriteString(line.text)
		if line.count > 1 {
			fmt.Fprintf(&out, " (repeated %d times)", line.count)
		}
		out.WriteByte('\n')
	}
	out.WriteString(textsanitize.Safe(string(d.partial), false))
	return strings.TrimSpace(out.String())
}
