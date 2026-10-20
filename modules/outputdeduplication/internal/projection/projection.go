// Package projection owns protocol-neutral reference text for a request pass.
package projection

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/modules/outputdeduplication/internal/matcher"
)

type Service struct{}

type Pass struct {
	index   *matcher.Index
	sources int
}

type Result struct {
	Text                                    string
	Occurrences                             uint64
	RepeatedLines, BodyBytes, RepeatedBytes int
}

func (Service) NewPass() *Pass { return &Pass{index: matcher.New()} }
func (p *Pass) Close()         { p.index.Close() }

// Source: internal/router/output_dedupe.go:33:76@b9a7b3fe.
func (p *Pass) Text(header, body string) Result {
	result := Result{BodyBytes: len(body)}
	if len(body) < matcher.Threshold {
		result.Text = header + body
		return result
	}
	projection := p.index.Project(body)
	if len(projection.Spans) > 0 {
		result.Occurrences++
	}
	verbatim := len(body)
	for _, span := range projection.Spans {
		verbatim -= span.End - span.Start
		result.RepeatedBytes += span.End - span.Start
		result.RepeatedLines += strings.Count(strings.TrimSuffix(body[span.Start:span.End], "\n"), "\n") + 1
	}
	var text strings.Builder
	text.WriteString(header)
	if verbatim > 0 {
		p.sources++
		p.index.Add(p.sources, projection)
		// Anchor sources before any later repeat to keep growing prefixes stable.
		fmt.Fprintf(&text, "[O%d]\n", p.sources)
	}
	start := 0
	for _, span := range projection.Spans {
		text.WriteString(body[start:span.Start])
		fmt.Fprintf(&text, "[same as O%d", span.Source)
		if !span.Whole {
			if span.FirstLine == span.LastLine {
				fmt.Fprintf(&text, " L%d", span.FirstLine)
			} else {
				fmt.Fprintf(&text, " L%d-%d", span.FirstLine, span.LastLine)
			}
		}
		text.WriteByte(']')
		if body[span.End-1] == '\n' {
			text.WriteByte('\n')
		}
		start = span.End
	}
	text.WriteString(body[start:])
	result.Text = text.String()
	return result
}
