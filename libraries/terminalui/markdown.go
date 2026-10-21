package terminalui

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/libraries/diffrender"
	markdownsyntax "github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui/mermaid"
)

// Source: internal/ui/activity/paint.go:96:1727@[b5ba67e3] shared Markdown painting.
// Painter owns shared Markdown configuration and a viewer-local syntax cache.
type Painter struct {
	Theme  diffrender.Theme
	Colors Colors
	Syntax diffrender.Renderer
	// LayoutOnly preserves geometry without syntax or copy annotations.
	LayoutOnly bool
	CopySource bool
	CopyScope  uint64
	// FileLink authorizes local destinations using the consumer's workspace.
	FileLink func(target string) bool
	// DeferFileLinks keeps local candidates private until visible-row admission.
	DeferFileLinks bool
}

const (
	Dim   = diffrender.Subtle
	Undim = diffrender.SubtleReset
	Reset = "\x1b[0m"
	// AutoHighlightBytes bounds language detection on untyped content.
	AutoHighlightBytes = 8 << 10
)

func languagePath(lang string) string {
	switch strings.ToLower(lang) {
	case "":
		return ""
	case "bash", "sh", "shell", "console":
		return "command.sh"
	case "javascript", "js":
		return "source.js"
	case "typescript", "ts":
		return "source.ts"
	case "python", "py":
		return "source.py"
	case "perl":
		return "source.pl"
	case "ruby", "rb":
		return "source.rb"
	}
	return "source." + strings.ToLower(lang)
}

// Highlight colors source by language, falling back to exact plain text.
func (p *Painter) Highlight(lang, source string) []string {
	if p.LayoutOnly {
		if !strings.EqualFold(lang, "diff") {
			source = strings.TrimSuffix(source, "\n")
		}
		return strings.Split(source, "\n")
	}
	if strings.EqualFold(lang, "diff") {
		lines, err := p.Syntax.ColorDiff(context.Background(), p.Theme, source)
		if err != nil {
			return strings.Split(source, "\n")
		}
		return lines
	}
	lines, err := p.Syntax.ColorSource(context.Background(), p.Theme, languagePath(lang), source)
	if err != nil || len(lines) == 0 {
		return strings.Split(source, "\n")
	}
	return lines
}

// Wrap restores the complete SGR state on continuation rows,
// which may be painted independently after a gutter or terminal reset.
func Wrap(text string, width int, hard bool) []string {
	width = max(1, width)
	var wrapped string
	if hard {
		wrapped = ansi.Hardwrap(text, width, true)
	} else {
		wrapped = ansi.Wrap(text, width, "")
	}
	var lines []string
	var style uv.Style
	var link uv.Link
	parser := ansi.GetParser()
	defer func() {
		parser.SetHandler(ansi.Handler{})
		ansi.PutParser(parser)
	}()
	parser.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd == 'm' {
			uv.ReadStyle(params, &style)
		}
	}, HandleOsc: func(cmd int, data []byte) {
		if cmd == 8 {
			uv.ReadLink(data, &link)
		}
	}})
	carry := ""
	for line := range strings.SplitSeq(wrapped, "\n") {
		plain := ansi.Strip(line)
		if blanks := len(plain) - len(strings.TrimRight(plain, " ")); blanks > 0 && ansi.StringWidth(plain) > width {
			// ansi.Wrap keeps the blank it breaks at; drop it, keeping escapes.
			line = ansi.Truncate(line, ansi.StringWidth(plain)-blanks, "")
		}
		row := carry + line
		parser.Parse([]byte(line))
		// Padding and the next row's gutter are outside the styled content.
		if !style.IsZero() {
			row += Reset
		}
		if !link.IsZero() {
			row += "\x1b]8;;\x1b\\"
		}
		lines = append(lines, row)
		carry = style.String()
		if style.IsZero() {
			// Enclosing bands recognize this canonical reset to reapply their background.
			carry = Reset
		}
		if !link.IsZero() {
			carry += ansi.SetHyperlink(link.URL, link.Params)
		}
	}
	return lines
}

// Hang places a styled label after a lead, wrapping continuations under the label.
func Hang(lead, label string, width int) []string {
	indent := ansi.StringWidth(lead)
	if label == "" {
		return []string{ansi.Truncate(lead, width, "…")}
	}
	if indent > width/2 {
		lines := []string{ansi.Truncate(lead, width, "…")}
		for _, line := range Wrap(label, width-2, false) {
			lines = append(lines, "  "+line)
		}
		return lines
	}
	var lines []string
	for i, line := range Wrap(label, width-indent, false) {
		if i == 0 {
			lines = append(lines, lead+line)
		} else {
			lines = append(lines, strings.Repeat(" ", indent)+line)
		}
	}
	return lines
}

// Inline renders the supported Markdown inline subset.
func (p *Painter) Inline(line string) string {
	line = textsanitize.Safe(line, false)
	var out strings.Builder
	bold := false
	for i := 0; i < len(line); {
		if line[i] == '[' {
			if label, target, end, ok := markdownLink(line[i:]); ok {
				if p.linkAllowed(target) {
					out.WriteString(p.inlineLink(label, target))
				} else {
					out.WriteString(label)
				}
				i += end
				continue
			}
		}
		if code, end, ok := markdownsyntax.CodeSpan(line, i); ok {
			if rawFilePath(code) && p.linkAllowed(code) {
				out.WriteString(p.inlineLink(code, code))
				i = end
				continue
			}
			// Inline spans have no language tag. Detect from their content,
			// retaining the accent for plain text and uncolored tokens.
			highlighted := code
			if !p.LayoutOnly && len(code) <= AutoHighlightBytes {
				highlighted = strings.Join(p.Highlight("", code), "\n")
			}
			out.WriteString(p.Theme.Accent() + strings.ReplaceAll(highlighted, "\x1b[39m", p.Theme.Accent()) + "\x1b[39m")
			i = end
			continue
		}
		if strings.HasPrefix(line[i:], "**") {
			bold = !bold
			if bold {
				out.WriteString("\x1b[1m")
			} else {
				out.WriteString(Undim)
			}
			i += 2
			continue
		}
		if i == 0 || strings.ContainsRune(" \t<([\"'", rune(line[i-1])) || i >= 2 && line[i-2:i] == "**" {
			end := len(line)
			if stop := strings.IndexAny(line[i:], " \t<>[](){}\"'`*"); stop >= 0 {
				end = i + stop
			}
			path := strings.TrimRight(line[i:end], ".,;!?")
			if rawFilePath(path) && p.linkAllowed(path) {
				out.WriteString(p.inlineLink(path, path))
				i += len(path)
				continue
			}
		}
		out.WriteByte(line[i])
		i++
	}
	if bold {
		out.WriteString(Undim)
	}
	return out.String()
}

func (p *Painter) linkAllowed(target string) bool {
	return strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") || p.FileLink != nil && p.FileLink(target)
}

func (p *Painter) inlineLink(label, target string) string {
	if p.DeferFileLinks && !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return "\x1b]8;;" + fileLinkCandidate + url.QueryEscape(target) + "\x1b\\" + label + "\x1b]8;;\x1b\\"
	}
	if strings.HasPrefix(target, "/") {
		path := url.URL{Scheme: "file", Path: target}
		target = path.String()
	}
	return "\x1b]8;;" + target + "\x1b\\" + p.Theme.Accent() + "\x1b[4m" + label + "\x1b[24;39m\x1b]8;;\x1b\\"
}

// Recognize file-shaped operands without reading the filesystem during painting.
// Canonical agent names are identifiers, not file links.
func rawFilePath(path string) bool {
	if path == "" || strings.ContainsAny(path[:1], "#!@$?") || strings.ContainsAny(path, "\x00\r\n\x1b\"'(){}[]=*;") || strings.Contains(path, "://") {
		return false
	}
	base := path
	if colon := strings.LastIndexByte(base, ':'); colon >= 0 {
		base = base[:colon]
	}
	name := base[strings.LastIndexByte(base, '/')+1:]
	file := strings.Contains(name, ".") && strings.Trim(name, ". ") != ""
	if strings.HasPrefix(base, "/") {
		first, _, _ := strings.Cut(base[1:], "/")
		_, ordinal := strconv.Atoi(first)
		return name != "" && ordinal != nil && (first != "root" || file)
	}
	first, _, _ := strings.Cut(base, "/")
	return !strings.Contains(base, ":") && !strings.ContainsAny(first, " \t") && name != "" && (strings.Contains(base, "/") || file)
}

// Parse local paths, file URLs and HTTP(S) URLs independently of clickability,
// so missing destinations retain the same visible label and copy annotations.
func markdownLink(s string) (label, target string, end int, ok bool) {
	close, depth := -1, 0
	for i, c := range s {
		if c == '[' {
			depth++
		}
		if c == ']' {
			depth--
			if depth == 0 {
				close = i
				break
			}
		}
	}
	if close < 2 || s[0] != '[' || close+1 >= len(s) || s[close+1] != '(' {
		return
	}
	rest := s[close+2:]
	if strings.HasPrefix(rest, "<") {
		if i := strings.Index(rest, ">)"); i >= 0 {
			target, end = rest[1:i], close+2+i+2
		}
	} else if i := strings.IndexByte(rest, ')'); i >= 0 {
		target, end = rest[:i], close+2+i+1
	}
	if end == 0 || strings.ContainsAny(target, "\x00\x1b\r\n") {
		return "", "", 0, false
	}
	if strings.HasPrefix(target, "/") || strings.HasPrefix(target, "file://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") {
		return s[1:close], target, end, true
	}
	path := target
	if colon := strings.LastIndexByte(path, ':'); colon >= 0 {
		from, to, ranged := strings.Cut(path[colon+1:], "-")
		first, err := strconv.Atoi(from)
		last, lastErr := strconv.Atoi(to)
		if err == nil && first > 0 && (!ranged || lastErr == nil && last >= first) {
			path = path[:colon]
		}
	}
	// Raw relative operands keep literal URL punctuation. Only an initial
	// fragment or URI scheme is non-file syntax.
	if path == "" || strings.HasPrefix(path, "#") || strings.HasPrefix(path, "?") || strings.Contains(strings.SplitN(path, "/", 2)[0], ":") {
		return "", "", 0, false
	}
	return s[1:close], target, end, true
}

// Markdown paints authored text. Subdued presentation preserves paragraph
// indentation on soft wraps and omits heading colors and generated heading rules.
func (p *Painter) Markdown(text string, width int, subdued bool) []string {
	copying := p.CopySource && !p.LayoutOnly
	identity := uint64(0)
	if copying {
		identity = CopyID(text, p.CopyScope)
	}
	annotate := func(rows []string, source, prefix string, gutter, index int) []string {
		if !copying {
			return rows
		}
		f := copyInline(source)
		f.ID = CopyID(strconv.Itoa(index), identity)
		f.Prefix = prefix
		return p.CopyWrapped(rows, f, gutter)
	}
	annotateHang := func(rows []string, source, prefix string, gutter, index int) []string {
		if !copying {
			return rows
		}
		if gutter > width/2 && len(rows) > 1 {
			rows[0] = CopyDecoration(rows[0])
			annotate(rows[1:], source, prefix, 2, index)
			return rows
		}
		return annotate(rows, source, prefix, gutter, index)
	}
	var lines, program, quote []string
	flushQuote := func() {
		if len(quote) > 0 {
			lines = append(lines, p.Quote(strings.Join(quote, "\n"), width)...)
			quote = nil
		}
	}
	fence, lang := "", ""
	fenceIdentity := uint64(0)
	source := strings.Split(text, "\n")
	for i := 0; i < len(source); i++ {
		raw := source[i]
		line := textsanitize.Safe(raw, false)
		if fence != "" {
			if !markdownsyntax.ClosesFence(line, fence) {
				program = append(program, raw)
				continue
			}
			body := strings.Join(program, "\n")
			var diagram []string
			if strings.EqualFold(lang, "mermaid") {
				diagram, _ = mermaid.Render(textsanitize.Safe(body, false), width)
			}
			if len(diagram) > 0 {
				lines = append(lines, diagram...)
			} else {
				lines = append(lines, p.fenced(lang, body, width, fenceIdentity)...)
			}
			fence, program = "", nil
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) <= 3 && strings.HasPrefix(trimmed, ">") {
			quote = append(quote, strings.TrimPrefix(trimmed[1:], " "))
			continue
		}
		flushQuote()
		if delimiter, ok := markdownsyntax.FenceDelimiter(line); ok {
			fence, lang = delimiter, strings.TrimSpace(strings.TrimLeft(line, " ")[len(delimiter):])
			if copying {
				fenceIdentity = CopyID(strconv.Itoa(i), identity)
			}
			continue
		}
		if table, consumed := parseMarkdownTable(source[i:]); consumed > 0 {
			if copying {
				table.copyID = CopyID(strings.Join(source[i:i+consumed], "\n"), identity+uint64(i))
			}
			lines = append(lines, p.markdownTable(table, max(1, width))...)
			i += consumed - 1
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		// Headings follow Bun's terminal Markdown: one bold color per level and
		// a rule under the first two, from three cells up to the full width.
		heading := func(level int, prefix, title, closing, underline string) {
			style, color := "\x1b[1m", ""
			if !subdued {
				color = p.Theme.HeadingColor(level)
			}
			style += color
			// Inline spans end with their own resets; restore the heading style after each.
			inline := strings.NewReplacer(Reset, Reset+style, Undim, Undim+"\x1b[1m", "\x1b[39m", "\x1b[39m"+color, "\x1b[24;39m", "\x1b[24;39m"+color).Replace(p.Inline(title))
			rows := Wrap(style+inline+Reset, width, false)
			if copying {
				f := copyInline(title)
				f.ID, f.Prefix = CopyID(strconv.Itoa(i), identity), prefix
				if closing != "" {
					f.Styles = append(f.Styles, copyStyle{0, len(f.Text), "", closing, -1})
				}
				rows = p.CopyWrapped(rows, f, 0)
			}
			lines = append(lines, rows...)
			if level > 2 || subdued {
				if underline != "" {
					i++
				}
				return
			}
			cells := 0
			for _, row := range rows {
				cells = max(cells, ansi.StringWidth(row))
			}
			rule := "─"
			if level == 1 {
				rule = "═"
			}
			row := Dim + strings.Repeat(rule, min(width, max(3, cells))) + Reset
			if copying && underline != "" {
				row = copyTag(CopyFragment{ID: CopyID(strconv.Itoa(i+1), identity), Text: underline, Width: ansi.StringWidth(row)}) + row
			} else if copying {
				row = CopyDecoration(row)
			}
			lines = append(lines, row)
			if underline != "" {
				i++
			}
		}
		if len(indent) <= 3 {
			if level, prefix, title, closing, ok := atxHeading(trimmed); ok {
				heading(level, prefix, title, closing, "")
				continue
			}
			if i+1 < len(source) && trimmed != "" && !strings.HasPrefix(trimmed, "- ") && !strings.HasPrefix(trimmed, "* ") {
				if level, underline, ok := setextUnderline(textsanitize.Safe(source[i+1], false)); ok {
					heading(level, "", strings.TrimRight(trimmed, " \t"), "", underline)
					continue
				}
			}
		}
		switch {
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			lines = append(lines, annotateHang(Hang(indent+Dim+"•"+Undim+" ", p.Inline(trimmed[2:]), width), trimmed[2:], indent+trimmed[:2], ansi.StringWidth(indent)+2, i)...)
		default:
			if subdued && indent != "" {
				lines = append(lines, annotateHang(Hang(indent, p.Inline(trimmed), width), trimmed, indent, ansi.StringWidth(indent), i)...)
			} else {
				lines = append(lines, annotate(Wrap(p.Inline(line), width, false), line, "", 0, i)...)
			}
		}
	}
	flushQuote()
	if fence != "" {
		lines = append(lines, p.fenced(lang, strings.Join(program, "\n"), width, fenceIdentity)...)
	}
	// Providers may open or close a message with blank lines; they only pad the block.
	for len(lines) > 0 && ansi.Strip(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && ansi.Strip(lines[0]) == "" {
		lines = lines[1:]
	}
	return lines
}

// atxHeading parses a CommonMark ATX heading: one to six markers followed by
// whitespace or the line end, and an optional closing marker sequence.
func atxHeading(line string) (level int, prefix, title, closing string, ok bool) {
	level = len(line) - len(strings.TrimLeft(line, "#"))
	rest := line[level:]
	if level == 0 || level > 6 || rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", "", "", false
	}
	title = strings.TrimLeft(rest, " \t")
	prefix = line[:len(line)-len(title)]
	title = strings.TrimRight(title, " \t")
	if open := strings.TrimRight(title, "#"); open == "" || strings.HasSuffix(open, " ") || strings.HasSuffix(open, "\t") {
		title = strings.TrimRight(open, " \t")
		closing = strings.TrimRight(line[len(prefix)+len(title):], " \t")
	}
	return level, prefix, title, closing, true
}

// setextUnderline reports a line of only = or - markers that turns the
// preceding paragraph line into a level one or two heading.
func setextUnderline(line string) (level int, underline string, ok bool) {
	underline = strings.TrimRight(line, " \t")
	marks := strings.TrimLeft(underline, " ")
	if len(underline)-len(marks) > 3 || marks == "" || strings.Trim(marks, marks[:1]) != "" || marks[0] != '=' && marks[0] != '-' {
		return 0, "", false
	}
	if marks[0] == '=' {
		return 1, underline, true
	}
	return 2, underline, true
}

// Quote keeps a visible bar on every wrapped row and renders the quoted
// Markdown normally, including lists, nested quotes, and fenced code. The bar
// is heavier than feed gutters so a quote reads as quoted, not as a thread.
func (p *Painter) Quote(text string, width int) []string {
	rows := p.Markdown(text, max(1, width-2), false)
	if len(rows) == 0 {
		rows = []string{""}
	}
	for i, row := range rows {
		if p.CopySource && !p.LayoutOnly {
			clean, spans := ExtractCopy(row)
			for j := range spans {
				spans[j].Quote = "> " + spans[j].Quote
			}
			row = AttachCopy(clean, spans)
		}
		rows[i] = ansi.Truncate(Dim+"▎"+Undim+" "+row, max(1, width), "")
	}
	return rows
}

// fenced sets authored code on a padded fill so it cannot be mistaken for a
// quote. An undetected theme (as behind mosh, which answers no OSC 11 query)
// gets a self-contained dark block: highlighting already assumes dark, and an
// explicit foreground keeps plain text readable on either terminal theme.
func (p *Painter) fenced(lang, source string, width int, identity uint64) []string {
	fill, ink := p.CodeBackground(), ""
	if fill == "" {
		fill, ink = "\x1b[48;2;32;35;40m", "\x1b[38;2;230;237;243m"
	}
	var lines []string
	var sources []string
	if p.CopySource && !p.LayoutOnly {
		sources = strings.Split(source, "\n")
	}
	for index, line := range p.Highlight(lang, textsanitize.Safe(source, false)) {
		parts := Wrap(line, width-2, true)
		if len(sources) > 0 {
			parts = p.CopyWrapped(parts, copyCode(sources[index], CopyID(strconv.Itoa(index), identity)), 0)
		}
		for _, part := range parts {
			if ink != "" {
				part = strings.ReplaceAll(part, "\x1b[39m", ink)
			}
			pad := strings.Repeat(" ", max(0, width-1-ansi.StringWidth(part)))
			row := fill + ink + " " + strings.ReplaceAll(part, Reset, Reset+fill+ink) + pad + "\x1b[49m"
			if ink != "" {
				row += "\x1b[39m"
			}
			lines = append(lines, row)
		}
	}
	return lines
}

// CodeBackground lifts the reported terminal background slightly toward its
// foreground side, falling back to fixed fills for a theme known only by name.
func (p *Painter) CodeBackground() string {
	if p.Colors.HasBackground {
		bg, to := p.Colors.Background, uint8(255)
		if p.Theme == diffrender.LightTheme {
			to = 0
		}
		channel := func(from uint8) int { return int(float64(from) + (float64(to)-float64(from))*.07 + .5) }
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", channel(bg.R), channel(bg.G), channel(bg.B))
	}
	switch p.Theme {
	case diffrender.LightTheme:
		return "\x1b[48;2;242;243;245m"
	case diffrender.DarkTheme:
		return "\x1b[48;2;32;35;40m"
	}
	return ""
}

// MarkdownPreview summarizes source without table borders or diagram decoration.
func (p *Painter) MarkdownPreview(text string) string {
	// Summaries describe tables and diagrams, not their decorative borders.
	source := strings.Split(strings.TrimSpace(text), "\n")
	source = source[:min(len(source), 3)]
	for i, line := range source {
		for {
			trimmed := strings.TrimLeft(line, " ")
			if len(line)-len(trimmed) > 3 || !strings.HasPrefix(trimmed, ">") {
				break
			}
			line = strings.TrimPrefix(trimmed[1:], " ")
		}
		source[i] = line
	}
	if fence, ok := markdownsyntax.FenceDelimiter(source[0]); ok && strings.EqualFold(strings.TrimSpace(strings.TrimLeft(source[0], " ")[len(fence):]), "mermaid") {
		for _, line := range source[1:] {
			if markdownsyntax.ClosesFence(line, fence) {
				break
			}
			if line = strings.TrimSpace(line); line != "" {
				return "Mermaid: " + ansi.Strip(line)
			}
		}
		return "Mermaid diagram"
	}
	if table, consumed := parseMarkdownTable(source); consumed > 0 {
		cells := make([]string, len(table.align))
		for c, header := range table.rows[0] {
			cells[c] = header
			if len(table.rows) > 1 {
				cells[c] += ": " + table.rows[1][c]
			}
		}
		return ansi.Strip(p.Inline(strings.Join(cells, " · ")))
	}
	for _, line := range p.Markdown(text, 1<<16, false) {
		if plain := strings.TrimSpace(strings.TrimPrefix(ansi.Strip(line), "│")); plain != "" {
			return strings.TrimSpace(strings.TrimPrefix(plain, "• "))
		}
	}
	return ""
}
