// Package projection owns protocol-neutral reference text for a request pass.
package projection

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/modules/outputdeduplication/internal/matcher"
)

type Service struct{}

const minimumTokenSavings = 32

type Pass struct {
	index   *matcher.Index
	sources int
	savings *Savings
}

type Result struct {
	Text                                    string
	Occurrences                             uint64
	RepeatedLines, BodyBytes, RepeatedBytes int
}

func (Service) NewPass(savings *Savings) *Pass {
	return &Pass{index: matcher.New(), savings: savings}
}
func (p *Pass) Close() { p.index.Close() }

// Source: internal/router/output_dedupe.go:33:76@b9a7b3fe.
func (p *Pass) Text(header, body string) Result {
	result := Result{BodyBytes: len(body)}
	if len(body) < matcher.Threshold {
		result.Text = header + body
		return result
	}
	projection := p.index.Project(body)
	accepted := projection.Spans[:0]
	markers := make([]string, 0, len(projection.Spans))
	for _, span := range projection.Spans {
		var marker strings.Builder
		fmt.Fprintf(&marker, "[same as O%d", span.Source)
		if !span.Whole {
			if span.FirstLine == span.LastLine {
				fmt.Fprintf(&marker, " L%d", span.FirstLine)
			} else {
				fmt.Fprintf(&marker, " L%d-%d", span.FirstLine, span.LastLine)
			}
		}
		marker.WriteByte(']')
		if body[span.End-1] == '\n' {
			marker.WriteByte('\n')
		}
		reference := marker.String()
		saved := p.savings.count(body[span.Start:span.End]) - p.savings.count(reference)
		if !p.savings.failed && saved >= minimumTokenSavings {
			accepted = append(accepted, span)
			markers = append(markers, reference)
		}
	}
	projection.Spans = accepted
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
	for i, span := range projection.Spans {
		text.WriteString(body[start:span.Start])
		text.WriteString(markers[i])
		start = span.End
	}
	text.WriteString(body[start:])
	result.Text = text.String()
	return result
}
