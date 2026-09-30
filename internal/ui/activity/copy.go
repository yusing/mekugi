package activity

import (
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/yusing/mekugi/internal/livediff"
)

// Source: codex-rs/tui/src/markdown_copy.rs:301:340@[75a714843] selection.
// Copy annotations follow logical fragments, not terminal padding.
// The private OSC envelope is an in-process row carrier only. ExtractCopy removes
// it before terminal delivery. Self-contained fragments survive cached runs,
// clipping, pinned copies and frozen selection snapshots without a global store.
const copyEnvelope = "\x1b]777;mekugi-copy;"

func copyID(text string, scope uint64) uint64 {
	h := fnv.New64a()
	h.Write(strconv.AppendUint(nil, scope, 10))
	h.Write([]byte{0})
	h.Write([]byte(text))
	return h.Sum64()
}

type copyStyle struct {
	Start, End  int
	Open, Close string
	Identity    int // Logical span start, stable through clipping and wrapping.
}

type CopyFragment struct {
	ID                uint64
	Text, Gap, Prefix string
	Quote             string
	Offset, Width     int
	Styles            []copyStyle
	Tabs              []int // Display-byte positions of source tabs (four cells).
	Code, Hard        bool
	Table             uint64
	Row, Cell         int
	Align             string
	Rule              bool
	Omit              bool
}

// CopySpan is positioned in terminal cells, while its source uses UTF-8 bytes.
type CopySpan struct {
	Column int
	CopyFragment
}

func copyInline(source string) CopyFragment {
	source = livediff.Safe(source, false)
	var f CopyFragment
	var text strings.Builder
	text.Grow(len(source))
	bold := -1
	for i := 0; i < len(source); {
		if source[i] == '[' {
			if label, target, end, ok := liveActivityLink(source[i:]); ok {
				start := text.Len()
				text.WriteString(label)
				if strings.ContainsAny(target, " \t\n()") {
					target = "<" + target + ">"
				}
				f.Styles = append(f.Styles, copyStyle{start, text.Len(), "[", "](" + target + ")", start + 1})
				i += end
				continue
			}
		}
		if code, end, ok := liveActivityCodeSpan(source, i); ok {
			start := text.Len()
			text.WriteString(code)
			f.Styles = append(f.Styles, copyStyle{start, text.Len(), "`", "`", start + 1})
			i = end
			continue
		}
		if strings.HasPrefix(source[i:], "**") {
			if bold < 0 {
				bold = text.Len()
			} else {
				f.Styles = append(f.Styles, copyStyle{bold, text.Len(), "**", "**", bold + 1})
				bold = -1
			}
			i += 2
			continue
		}
		text.WriteByte(source[i])
		i++
	}
	if bold >= 0 {
		f.Styles = append(f.Styles, copyStyle{bold, text.Len(), "**", "**", bold + 1})
	}
	f.Text = text.String()
	f.Hard = strings.HasSuffix(source, "  ")
	return f
}

func copyTag(f CopyFragment) string {
	data, err := json.Marshal(&f, json.OmitZeroStructFields(true))
	if err != nil {
		return ""
	}
	return copyEnvelope + base64.RawStdEncoding.EncodeToString(data) + "\x1b\\"
}

// copyWrapped projects the already-rendered fragments onto one logical source.
// The renderer's known content gutter is excluded; wrapping gaps stay attached
// to the continuation but are copied only when both fragments are selected.
func (p *Painter) copyWrapped(rows []string, source CopyFragment, gutter int) []string {
	if !p.CopySource || p.LayoutOnly {
		return rows
	}
	offset := 0
	for i, row := range rows {
		plain := ansi.Strip(ansi.Cut(row, gutter, ansi.StringWidth(row)))
		// Code background fill and record punctuation are not source text.
		if !source.Code {
			plain = strings.TrimRight(plain, " ")
		}
		start := offset
		if plain != "" {
			if at := strings.Index(source.Text[offset:], plain); at >= 0 {
				start += at
			} else {
				// A narrow viewport may clip a grapheme or a generated suffix.
				plain = ansi.Strip(ansi.Truncate(source.Text[offset:], ansi.StringWidth(plain), ""))
			}
		}
		end := min(len(source.Text), start+len(plain))
		f := source
		f.Offset, f.Width = start, ansi.StringWidth(plain)
		f.Gap = source.Text[offset:start]
		if i == len(rows)-1 && strings.TrimSpace(source.Text[end:]) == "" {
			end = len(source.Text)
		}
		f.Text = source.Text[start:end]
		if f.Code {
			f.Width = min(ansi.StringWidth(f.Text), max(0, ansi.StringWidth(row)-gutter))
		}
		f.Tabs = nil
		for _, tab := range source.Tabs {
			if tab < end && tab+4 > start {
				f.Tabs = append(f.Tabs, tab-start)
			}
		}
		f.Styles = nil
		for _, style := range source.Styles {
			if style.End > start && style.Start < end {
				style.Start, style.End = max(0, style.Start-start), min(end-start, style.End-start)
				f.Styles = append(f.Styles, style)
			}
		}
		f.Hard = source.Hard && i == len(rows)-1
		rows[i] = ansi.Cut(row, 0, gutter) + copyTag(f) + ansi.Cut(row, gutter, ansi.StringWidth(row))
		offset = end
	}
	return rows
}

// ExtractCopy resolves the final composed row, including absolute cursor moves.
// No private annotation reaches the terminal emulator or external terminal.
func ExtractCopy(row string) (string, []CopySpan) {
	var out strings.Builder
	var spans []CopySpan
	column := 0
	var state byte
	parser := ansi.NewParser()
	for len(row) > 0 {
		seq, width, n, next := ansi.DecodeSequence(row, state, parser)
		if n == 0 {
			break
		}
		state, row = next, row[n:]
		if strings.HasPrefix(seq, copyEnvelope) {
			data, err := base64.RawStdEncoding.DecodeString(strings.TrimSuffix(seq[len(copyEnvelope):], "\x1b\\"))
			var f CopyFragment
			if err == nil && json.Unmarshal(data, &f) == nil {
				spans = append(spans, CopySpan{column, f})
			}
			continue
		}
		if strings.HasPrefix(seq, "\x1b[") && parser.Command() == 'G' {
			at, _ := parser.Param(0, 1)
			column = max(0, at-1)
		} else {
			column += width
		}
		out.WriteString(seq)
	}
	return out.String(), spans
}

// Clip selects whole graphemes, never terminal fill, and keeps genuine trailing
// whitespace when the selection reaches the logical fragment's visible end.
func (s CopySpan) Clip(left, right int) (CopyFragment, bool) {
	f := s.CopyFragment
	if f.Rule || f.Omit {
		return f, right > left && left < s.Column+f.Width && right > s.Column
	}
	if f.Width == 0 {
		return f, left <= s.Column && right >= s.Column
	}
	if right <= s.Column || left >= s.Column+f.Width {
		return f, false
	}
	start, end, column := len(f.Text), 0, s.Column
	for g := uniseg.NewGraphemes(f.Text); g.Next(); {
		a, b := g.Positions()
		next := column + g.Width()
		if next > left && column < right {
			start = min(start, a)
			end = b
		}
		column = next
	}
	if right >= s.Column+f.Width {
		end = len(f.Text)
	}
	if end < start {
		return f, false
	}
	f.Offset += start
	f.Text = f.Text[start:end]
	f.Gap = ""
	if start == 0 {
		f.Gap = s.Gap
	}
	f.Tabs = nil
	for _, tab := range s.Tabs {
		if tab < end && tab+4 > start {
			f.Tabs = append(f.Tabs, tab-start)
		}
	}
	f.Styles = nil
	for _, style := range s.Styles {
		if style.End > start && style.Start < end {
			style.Start, style.End = max(0, style.Start-start), min(end-start, style.End-start)
			f.Styles = append(f.Styles, style)
		}
	}
	f.Hard = f.Hard && end == len(s.Text)
	return f, true
}

func mergeCopy(parts []CopyFragment) CopyFragment {
	parts = slices.Clone(parts)
	slices.SortFunc(parts, func(a, b CopyFragment) int { return a.Offset - b.Offset })
	result := parts[0]
	result.Styles = slices.Clone(result.Styles)
	result.Tabs = slices.Clone(result.Tabs)
	for _, part := range parts[1:] {
		end := result.Offset + len(result.Text)
		if part.Offset+len(part.Text) <= end {
			continue
		} // Repeated narrow-record labels.
		if part.Offset < end {
			overlap := end - part.Offset
			part.Text = part.Text[overlap:]
			part.Offset = end
			var styles []copyStyle
			for _, style := range part.Styles {
				if style.End > overlap {
					style.Start = max(0, style.Start-overlap)
					style.End -= overlap
					styles = append(styles, style)
				}
			}
			part.Styles = styles
		}
		gap := ""
		if part.Offset > end {
			gap = part.Gap
		}
		base := len(result.Text) + len(gap)
		result.Text += gap + part.Text
		for _, tab := range part.Tabs {
			result.Tabs = append(result.Tabs, tab+base)
		}
		for _, style := range part.Styles {
			style.Start += base
			style.End += base
			result.Styles = append(result.Styles, style)
		}
		result.Hard = part.Hard
	}
	// Coalesce wrappers split by visual wrapping before balancing Markdown.
	slices.SortFunc(result.Styles, func(a, b copyStyle) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		return b.End - a.End
	})
	for i := 0; i < len(result.Styles); i++ {
		for j := i + 1; j < len(result.Styles); j++ {
			a, b := result.Styles[i], result.Styles[j]
			if a.Identity == b.Identity && a.Open == b.Open && a.Close == b.Close && a.End <= b.Start && strings.TrimSpace(result.Text[a.End:b.Start]) == "" {
				result.Styles[i].End = b.End
				result.Styles = append(result.Styles[:j], result.Styles[j+1:]...)
				j--
			}
		}
	}
	return result
}

func renderCopy(f CopyFragment, table bool) string {
	if f.Code {
		text := f.Text
		tabs := slices.Clone(f.Tabs)
		slices.Sort(tabs)
		tabs = slices.Compact(tabs)
		for i := len(tabs) - 1; i >= 0; i-- {
			at := tabs[i]
			if at < 0 || at+4 > len(f.Text) {
				continue
			}
			text = text[:at] + "\t" + text[at+4:]
		}
		return text
	}
	// Balance wrappers from the final clipped content, not the original span.
	// Source: codex-rs/tui/src/markdown_copy.rs:129:177@[75a714843] render.
	f.Styles = slices.Clone(f.Styles)
	for i := range f.Styles {
		style := &f.Styles[i]
		body := f.Text[style.Start:style.End]
		if strings.HasPrefix(style.Open, "`") {
			delimiter := "`"
			for strings.Contains(body, delimiter) {
				delimiter += "`"
			}
			padding := ""
			if strings.Trim(body, " ") != "" && (strings.HasPrefix(body, "`") || strings.HasSuffix(body, "`") || strings.HasPrefix(body, " ") || strings.HasSuffix(body, " ")) {
				padding = " "
			}
			style.Open, style.Close = delimiter+padding, padding+delimiter
		} else if style.Open == "**" {
			trimmed := strings.TrimFunc(body, unicode.IsSpace)
			if trimmed == "" {
				style.Open, style.Close = "", ""
				continue
			}
			style.Start += len(body) - len(strings.TrimLeftFunc(body, unicode.IsSpace))
			style.End = style.Start + len(trimmed)
		}
	}
	var out strings.Builder
	for pos := 0; pos <= len(f.Text); pos++ {
		for i := len(f.Styles) - 1; i >= 0; i-- {
			if f.Styles[i].End == pos {
				out.WriteString(f.Styles[i].Close)
			}
		}
		for _, style := range f.Styles {
			if style.Start == pos {
				out.WriteString(style.Open)
			}
		}
		if pos < len(f.Text) {
			out.WriteByte(f.Text[pos])
		}
	}
	text := out.String()
	if table {
		text = strings.ReplaceAll(text, "|", `\|`)
	}
	if f.Hard && !strings.HasSuffix(text, "  ") {
		text += "  "
	}
	return f.Prefix + text
}

// CopyText serializes only the visible, selected fragments. Table identity and
// logical cell coordinates reconcile wrapped grids and repeated record labels.
func CopyText(parts []CopyFragment) string {
	var out []string
	for i := 0; i < len(parts); {
		f := parts[i]
		if f.Omit {
			i++
			continue
		}
		j := i + 1
		if f.Table == 0 {
			for j < len(parts) && parts[j].ID == f.ID {
				j++
			}
			out = append(out, renderCopy(mergeCopy(parts[i:j]), false))
		} else {
			for j < len(parts) && parts[j].Table == f.Table {
				j++
			}
			out = append(out, copyTable(parts[i:j]))
		}
		i = j
	}
	return strings.Join(out, "\n")
}

// Source: codex-rs/tui/src/markdown_copy/table.rs:103:208@[75a714843] render.
func copyTable(parts []CopyFragment) string {
	type key struct{ row, cell int }
	cells := make(map[key][]CopyFragment)
	rule := false
	for _, f := range parts {
		if f.Rule {
			rule = true
			continue
		}
		k := key{f.Row, f.Cell}
		cells[k] = append(cells[k], f)
	}
	if len(cells) == 0 {
		return ""
	}
	if len(cells) == 1 && !rule {
		for _, parts := range cells {
			return renderCopy(mergeCopy(parts), false)
		}
	}
	align := parts[0].Align
	rows := map[int][]string{0: make([]string, len(align))}
	for k, fragments := range cells {
		if rows[k.row] == nil {
			rows[k.row] = make([]string, len(align))
		}
		rows[k.row][k.cell] = renderCopy(mergeCopy(fragments), true)
	}
	keys := make([]int, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out []string
	for _, r := range keys {
		out = append(out, "| "+strings.Join(rows[r], " | ")+" |")
		if r == 0 {
			var cols []string
			for _, a := range align {
				v := "---"
				if a == 'r' {
					v = "---:"
				}
				if a == 'c' {
					v = ":---:"
				}
				cols = append(cols, v)
			}
			out = append(out, fmt.Sprintf("| %s |", strings.Join(cols, " | ")))
		}
	}
	text := strings.Join(out, "\n")
	if prefix := parts[0].Quote; prefix != "" {
		text = prefix + strings.ReplaceAll(text, "\n", "\n"+prefix)
	}
	return text
}

// AttachCopy is used only while the router composes native panes. Public view
// rendering stays clean ANSI; the visible annotations remain a separate value.
func AttachCopy(row string, spans []CopySpan) string {
	for i := len(spans) - 1; i >= 0; i-- {
		s := spans[i]
		row = ansi.Cut(row, 0, s.Column) + copyTag(s.CopyFragment) + ansi.Cut(row, s.Column, ansi.StringWidth(row))
	}
	return row
}

func copyCode(source string, identity uint64) CopyFragment {
	f := CopyFragment{ID: identity, Code: true}
	for i, part := range strings.Split(source, "\t") {
		if i > 0 {
			f.Tabs = append(f.Tabs, len(f.Text))
			f.Text += "    "
		}
		f.Text += livediff.Safe(part, false)
	}
	return f
}

// CopyDecoration marks a generated heading as selectable geometry, not source.
func CopyDecoration(row string) string {
	return copyTag(CopyFragment{Omit: true, Width: ansi.StringWidth(row)}) + row
}

// MarkdownSource removes terminal controls without normalizing code tabs. The
// shared renderer expands them for display and retains their source identity.
func MarkdownSource(text string) string {
	parts := strings.Split(text, "\t")
	for i := range parts {
		parts[i] = livediff.Safe(parts[i], false)
	}
	return strings.Join(parts, "\t")
}
