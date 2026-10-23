// Source: apps/mekugi/internal/router/{app_server_composer,app_server_composer_history,app_server_clipboard,app_server_input_queue}.go@[e9da429a] draft editing and joining.
package composer

import (
	"fmt"
	"iter"
	"slices"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// Token binds one source span to an attachment. Its containing slice supplies
// the kind; unused payload fields stay empty. Literal lookalikes are unbound.
type Token struct {
	Start, End   int
	Path, Name   string
	Text, Source string
}

type Kind uint8

const (
	Image Kind = iota
	File
	Skill
	Selection
)

type Span struct {
	Start, End int
	Kind       Kind
}

// Draft is a detached value. Clone before editing a retained snapshot.
// CursorBack counts bytes after the caret, placing a new draft's caret at its end.
type Draft struct {
	Text                              string
	CursorBack                        int
	Images, Files, Skills, Selections []Token
	AnswerImages                      []string
	Attachments                       []string
	AttachmentNotice                  string
	InHistory                         bool
}

func (d Draft) Cursor() int { return len(d.Text) - min(d.CursorBack, len(d.Text)) }

func (d Draft) Clone() Draft {
	d.Images = slices.Clone(d.Images)
	d.Files = slices.Clone(d.Files)
	d.Skills = slices.Clone(d.Skills)
	d.Selections = slices.Clone(d.Selections)
	d.AnswerImages = slices.Clone(d.AnswerImages)
	d.Attachments = slices.Clone(d.Attachments)
	return d
}

// TextParts separates editable text from bound attachment labels. Even entries
// are editable; odd entries are the original labels, never source contents.
func (d Draft) TextParts() []string {
	parts := make([]string, 0, 2*len(d.Spans())+1)
	at := 0
	for _, span := range d.Spans() {
		parts = append(parts, d.Text[at:span.Start], d.Text[span.Start:span.End])
		at = span.End
	}
	return append(parts, d.Text[at:])
}

// WithTextParts replaces only ordinary text, retaining all prepared attachments.
func (d Draft) WithTextParts(parts []string) (Draft, error) {
	spans := d.Spans()
	if len(parts) != len(spans)+1 {
		return Draft{}, fmt.Errorf("The retained input changed; reopen its editor")
	}
	d = d.Clone()
	for i := len(spans); i >= 0; i-- {
		start, end := 0, len(d.Text)
		if i > 0 {
			start = spans[i-1].End
		}
		if i < len(spans) {
			end = spans[i].Start
		}
		d.Remove(start, end)
		d.InsertText(parts[i])
	}
	d.CursorBack = 0
	return d, nil
}

// Editable discards submission snapshots so recalling input takes fresh contents.
func (d Draft) Editable() Draft {
	return Draft{Text: d.Text, CursorBack: d.CursorBack,
		Images: slices.Clone(d.Images), Files: slices.Clone(d.Files),
		Skills: slices.Clone(d.Skills), Selections: slices.Clone(d.Selections)}
}

func (d *Draft) groups() [4]*[]Token {
	return [4]*[]Token{&d.Images, &d.Files, &d.Skills, &d.Selections}
}

func (d Draft) Spans() []Span {
	var spans []Span
	for kind, group := range d.groups() {
		for _, token := range *group {
			spans = append(spans, Span{token.Start, token.End, Kind(kind)})
		}
	}
	slices.SortFunc(spans, func(a, b Span) int { return a.Start - b.Start })
	return spans
}

// InsertText extends an already-recorded edit at the caret.
func (d *Draft) InsertText(text string) {
	at := d.Cursor()
	d.Text = d.Text[:at] + text + d.Text[at:]
	for _, group := range d.groups() {
		for i := range *group {
			token := &(*group)[i]
			if token.Start >= at {
				token.Start += len(text)
				token.End += len(text)
			}
		}
	}
}

// Remove deletes whole bindings intersecting the range and shifts the remainder.
// Callers select grapheme/token boundaries through Boundary or WordBoundary.
func (d *Draft) Remove(start, end int) {
	for _, group := range d.groups() {
		kept := (*group)[:0]
		for _, token := range *group {
			if token.Start < end && token.End > start {
				continue
			}
			if token.Start >= end {
				token.Start -= end - start
				token.End -= end - start
			}
			kept = append(kept, token)
		}
		*group = kept
	}
	d.Text = d.Text[:start] + d.Text[end:]
	d.CursorBack = len(d.Text) - start
	d.RenumberImages()
}

func (d *Draft) RenumberImages() {
	slices.SortFunc(d.Images, func(a, b Token) int { return a.Start - b.Start })
	at := d.Cursor()
	for i := range d.Images {
		image := &d.Images[i]
		label := fmt.Sprintf("[Image %d]", i+1)
		end := image.End
		delta := len(label) - (end - image.Start)
		d.Text = d.Text[:image.Start] + label + d.Text[end:]
		if at >= end {
			at += delta
		}
		for _, group := range d.groups() {
			for j := range *group {
				token := &(*group)[j]
				if token.Start >= end {
					token.Start += delta
					token.End += delta
				}
			}
		}
		image.End += delta
	}
	d.CursorBack = len(d.Text) - at
}

func Join(parts ...Draft) Draft {
	var joined Draft
	for _, part := range parts {
		if part.Text == "" {
			continue
		}
		if joined.Text != "" {
			joined.Text += "\n"
		}
		shift := len(joined.Text)
		joined.Text += part.Text
		joined.Attachments = append(joined.Attachments, part.Attachments...)
		joined.AnswerImages = append(joined.AnswerImages, part.AnswerImages...)
		if part.AttachmentNotice != "" {
			joined.AttachmentNotice = part.AttachmentNotice
		}
		destinations := joined.groups()
		for kind, group := range part.groups() {
			for _, token := range *group {
				token.Start += shift
				token.End += shift
				*destinations[kind] = append(*destinations[kind], token)
			}
		}
		joined.CursorBack = part.CursorBack
	}
	joined.RenumberImages()
	return joined
}

// Graphemes treats attachment edges as boundaries even beside combining marks.
func (d Draft) Graphemes() iter.Seq2[int, string] {
	return func(yield func(int, string) bool) {
		boundaries := []int{0, len(d.Text)}
		for _, span := range d.Spans() {
			boundaries = append(boundaries, span.Start, span.End)
		}
		slices.Sort(boundaries)
		boundaries = slices.Compact(boundaries)
		for i := 1; i < len(boundaries); i++ {
			start, end := boundaries[i-1], boundaries[i]
			g := uniseg.NewGraphemes(d.Text[start:end])
			for g.Next() {
				offset, _ := g.Positions()
				if !yield(start+offset, g.Str()) {
					return
				}
			}
		}
	}
}

func (d Draft) Boundary(at, direction int) int {
	for _, span := range d.Spans() {
		if direction < 0 && at > span.Start && at <= span.End {
			return span.Start
		}
		if direction > 0 && at >= span.Start && at < span.End {
			return span.End
		}
	}
	previous := 0
	for start, text := range d.Graphemes() {
		end := start + len(text)
		if direction < 0 && end >= at {
			return previous
		}
		if direction > 0 && end > at {
			return end
		}
		previous = end
	}
	return len(d.Text)
}

func (d Draft) WordBoundary(at int, backward bool) int {
	low, high := 0, len(d.Text)
	for _, span := range d.Spans() {
		if backward && at == span.End {
			return span.Start
		}
		if !backward && at == span.Start {
			return span.End
		}
		if span.End <= at {
			low = max(low, span.End)
		}
		if span.Start >= at {
			high = min(high, span.Start)
		}
	}
	separator := func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }
	if backward {
		for at > low {
			r, size := utf8.DecodeLastRuneInString(d.Text[:at])
			if !separator(r) {
				break
			}
			at -= size
		}
		for at > low {
			r, size := utf8.DecodeLastRuneInString(d.Text[:at])
			if separator(r) {
				break
			}
			at -= size
		}
	} else {
		for at < high {
			r, size := utf8.DecodeRuneInString(d.Text[at:])
			if !separator(r) {
				break
			}
			at += size
		}
		for at < high {
			r, size := utf8.DecodeRuneInString(d.Text[at:])
			if separator(r) {
				break
			}
			at += size
		}
	}
	for start, text := range d.Graphemes() {
		if start >= at {
			break
		}
		if end := start + len(text); at < end {
			if backward {
				return start
			}
			return end
		}
	}
	return at
}
