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
	exact   map[string]string
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

// Source gives explicitly supplied attachment/reload bodies one visible copy,
// including short sources below the ordinary tool-output savings threshold.
// Its request-local identities share the ordinary matcher anchor sequence.
func (p *Pass) Source(header, body string) Result {
	if body == "" {
		return Result{Text: header}
	}
	if reference := p.exact[body]; reference != "" {
		return repeatedSource(header, body, reference)
	}
	before := p.sources
	result := p.Text(header, body)
	if result.RepeatedBytes == 0 {
		if p.sources == before {
			p.sources++
			result.Text = fmt.Sprintf("%sO%d:\n%s", header, p.sources, body)
		}
		if p.exact == nil {
			p.exact = make(map[string]string)
		}
		p.exact[body] = referenceText(matcher.Span{Source: p.sources, Whole: true}, body)
	}
	return result
}

// Sources keeps framing independent of equality. A whole reload can refer to
// the visible chunks, and a chunked attachment can refer to an earlier whole read.
func (p *Pass) Sources(headers, bodies []string) []Result {
	results := make([]Result, len(bodies))
	whole := strings.Join(bodies, "")
	if reference := p.exact[whole]; reference != "" {
		for i, body := range bodies {
			results[i] = repeatedSource(headers[i], body, "")
		}
		results[0].Text += reference
		return results
	}
	var references []string
	for i, body := range bodies {
		results[i] = p.Source(headers[i], body)
		if reference := p.exact[body]; reference != "" {
			references = append(references, strings.TrimSuffix(reference, "\n"))
		}
	}
	if len(bodies) > 1 && len(references) == len(bodies) {
		p.exact[whole] = strings.Join(references, "\n")
		if strings.HasSuffix(whole, "\n") {
			p.exact[whole] += "\n"
		}
	}
	return results
}

func repeatedSource(header, body, reference string) Result {
	return Result{Text: header + reference, Occurrences: 1, BodyBytes: len(body), RepeatedBytes: len(body), RepeatedLines: strings.Count(strings.TrimSuffix(body, "\n"), "\n") + 1}
}

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
		run := body[span.Start:span.End]
		reference := referenceText(span, run)
		saved := p.savings.count(run) - p.savings.count(reference)
		if !p.savings.failed && saved >= minimumTokenSavings {
			accepted = append(accepted, span)
			markers = append(markers, reference)
		}
	}
	projection.Spans = accepted
	if len(projection.Spans) > 0 {
		result.Occurrences++
	}
	for _, span := range projection.Spans {
		result.RepeatedBytes += span.End - span.Start
		result.RepeatedLines += strings.Count(strings.TrimSuffix(body[span.Start:span.End], "\n"), "\n") + 1
	}
	var text strings.Builder
	text.WriteString(header)
	var anchor string
	var anchorTokens int64
	admissions := make(map[matcher.Span]bool)
	accept := func(span matcher.Span, run string) bool {
		if admitted, ok := admissions[span]; ok {
			return admitted
		}
		if anchor == "" {
			anchor = fmt.Sprintf("O%d:\n", span.Source)
			anchorTokens = p.savings.count(anchor)
		}
		saved := p.savings.count(run) - p.savings.count(referenceText(span, run)) - anchorTokens
		admitted := !p.savings.failed && saved >= minimumTokenSavings
		admissions[span] = admitted
		return admitted
	}
	add := result.RepeatedBytes < len(body) && p.index.CanAdd(p.sources+1, projection, accept)
	if add {
		// Anchor sources before any later repeat to keep growing prefixes stable.
		text.WriteString(anchor)
	}
	start := 0
	for i, span := range projection.Spans {
		text.WriteString(body[start:span.Start])
		text.WriteString(markers[i])
		start = span.End
	}
	text.WriteString(body[start:])
	result.Text = text.String()
	if edited, ok := p.editProjection(header, body, result.Text); ok {
		return edited
	}
	if add {
		p.index.Add(p.sources+1, projection, accept)
		p.sources++
	} else if result.RepeatedBytes < len(body) {
		// Rejected and seedless verbatim text still advances the window.
		p.index.Add(p.sources+1, projection, accept)
	}
	return result
}

// Both source admission and replacement pay for the same complete reference.
func referenceText(span matcher.Span, run string) string {
	var marker strings.Builder
	fmt.Fprintf(&marker, "See O%d", span.Source)
	if !span.Whole {
		if span.FirstLine == span.LastLine {
			fmt.Fprintf(&marker, ":%d", span.FirstLine)
		} else {
			fmt.Fprintf(&marker, ":%d-%d", span.FirstLine, span.LastLine)
		}
	}
	if strings.HasSuffix(run, "\n") {
		marker.WriteByte('\n')
	}
	return marker.String()
}
