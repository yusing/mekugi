package activity

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/ui/mermaid"
)

const (
	Dim   = livediff.Subtle
	Undim = livediff.SubtleReset
	Reset = "\x1b[0m"
	Green = "\x1b[38;5;114m"
	Red   = "\x1b[38;5;203m"
	Amber = "\x1b[38;5;214m"
)

// Painter lays out parsed blocks as terminal rows. Syntax colors
// come from the same renderer and theme as the live diff pane.
type Painter struct {
	Theme  livediff.Theme
	Colors Colors
	syntax livediff.Renderer
	Clock  func() time.Time // Optional presentation clock, shared with replay.
	// LayoutOnly skips syntax decoration while retaining exact text geometry.
	// The feed uses it for cold off-screen runs, never visible rows or dialogs.
	LayoutOnly bool
	// CopySource carries semantic annotations to the native viewport.
	CopySource bool
	CopyScope  uint64 // Stable native entry/block scope for cached source identities.
}

func (p *Painter) now() time.Time {
	if p.Clock != nil {
		return p.Clock()
	}
	return time.Now()
}

func VerbColor(verb string) string {
	first, _, _ := strings.Cut(verb, " ")
	switch first {
	case "Attached":
		return Green
	case "Attach":
		return Red
	case "Read", "Inspect", "View", "Open", "List", "Check", "Diff", "Status":
		return "\x1b[38;5;75m"
	case "Commit", "Committed", "Stage":
		return Hash
	case "Search", "Find", "Skill":
		return "\x1b[38;5;141m"
	case "Run", "Running", "Send", "Sleep", "Wait", "Still", "Stop":
		return Amber
	case "Edit", "Create", "Delete", "Update", "Write", "Move", "Rename":
		return Green
	case "MCP", "Tool", "Browse", "Generate":
		return "\x1b[38;5;80m"
	}
	return ""
}

// liveAgentGutter is the unbolded agent color used for feed gutters.
func Gutter(name string, theme livediff.Theme) string {
	if color := Color(name); color != "" {
		return strings.Replace(color, "1;", "", 1)
	}
	return theme.Accent()
}

func (p *Painter) Agent(name string) string {
	color := Color(name)
	if color == "" {
		color = "\x1b[1m" + p.Theme.Accent()
	}
	return color + AgentDisplayName(name) + Reset
}

func (p *Painter) recipient(name string) string {
	return Gutter(name, p.Theme) + AgentDisplayName(name) + Reset
}

func liveActivityLanguagePath(lang string) string {
	switch strings.ToLower(lang) {
	case "":
		return ""
	case "bash", "sh", "shell", "zsh", "console":
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

// highlight colors source by language, falling back to exact plain text.
func (p *Painter) Highlight(lang, source string) []string {
	if p.LayoutOnly {
		if !strings.EqualFold(lang, "diff") {
			source = strings.TrimSuffix(source, "\n")
		}
		return strings.Split(source, "\n")
	}
	if strings.EqualFold(lang, "diff") {
		lines, err := p.syntax.ColorDiff(context.Background(), p.Theme, source)
		if err != nil {
			return strings.Split(source, "\n")
		}
		return lines
	}
	lines, err := p.syntax.ColorSource(context.Background(), p.Theme, liveActivityLanguagePath(lang), source)
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

// Hang wraps label after lead, indenting continuation rows under the label.
func Hang(lead, label string, width int) []string { return liveActivityHang(lead, label, width) }

// hang places a styled label after a lead, wrapping continuations under the label.
func liveActivityHang(lead, label string, width int) []string {
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

func Path(path string) string {
	if i := strings.LastIndex(strings.TrimSuffix(path, "/"), "/"); i >= 0 {
		return Dim + path[:i+1] + Undim + "\x1b[1m" + path[i+1:] + Undim
	}
	return "\x1b[1m" + path + Undim
}

// code styles a code span according to the operation that carries it.
func (p *Painter) code(verb, code string) string {
	first, _, _ := strings.Cut(verb, " ")
	switch first {
	case "Skill":
		prefix := ""
		if strings.HasPrefix(code, "run ") || strings.HasPrefix(code, "run\t") {
			rest := strings.TrimLeft(code[3:], " \t")
			prefix, code = code[:len(code)-len(rest)], rest
		}
		end := strings.IndexAny(code, "/ \t")
		if end < 0 {
			end = len(code)
		}
		return prefix + "\x1b[1m" + code[:end] + Undim + code[end:]
	case "Run", "Send":
		return strings.Join(p.Highlight("bash", code), " ")
	case "Search":
		// Search operands are patterns, not shell programs. A shell lexer
		// miscolors regular-expression punctuation and can reset the verb color.
		return p.Theme.Accent() + code + "\x1b[39m"
	case "Read", "Edit", "Create", "Delete", "Update", "Write", "View", "Move", "Rename":
		return Path(code)
	case "MCP", "Tool":
		return "\x1b[38;5;80m" + code + "\x1b[39m"
	}
	return p.Theme.Accent() + code + "\x1b[39m"
}

func (p *Painter) Label(verb, label string) string {
	if slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, verb) {
		if head, source, ok := strings.CutLast(label, " · "); ok && !strings.ContainsRune(source, '`') {
			// Source metadata uses the same secondary foreground as UI chrome.
			return p.Label(verb, head) + Dim + " · " + source + Undim + "\x1b[39m"
		}
	}
	var out strings.Builder
	searchTargets := false
	for i := 0; i < len(label); {
		if code, end, ok := liveActivityCodeSpan(label, i); ok {
			if verb == "Search" && searchTargets {
				out.WriteString(Path(code))
			} else {
				out.WriteString(p.code(verb, code))
			}
			i = end
			continue
		}
		next := strings.IndexByte(label[i+1:], '`')
		text := label[i:]
		if next >= 0 {
			text = label[i : i+1+next]
		}
		if verb == "Search" && strings.Contains(text, " in ") {
			searchTargets = true
		}
		for j, word := range strings.Split(text, " ") {
			if j > 0 {
				out.WriteByte(' ')
			}
			switch {
			case len(word) > 1 && word[0] == '+':
				word = Green + word + "\x1b[39m"
			case len(word) > 1 && word[0] == '-':
				word = Red + word + "\x1b[39m"
			case word != "":
				word = Dim + word + Undim
			}
			out.WriteString(word)
		}
		i += len(text)
	}
	return out.String()
}

// inline renders the commentary Markdown inline subset.
func (p *Painter) Inline(line string) string {
	line = livediff.Safe(line, false)
	var out strings.Builder
	bold := false
	for i := 0; i < len(line); {
		if line[i] == '[' {
			if label, target, end, ok := liveActivityLink(line[i:]); ok {
				link := target
				if strings.HasPrefix(target, "/") {
					path := url.URL{Scheme: "file", Path: target}
					link = path.String()
				}
				out.WriteString("\x1b]8;;" + link + "\x1b\\" + p.Theme.Accent() + "\x1b[4m" + label + "\x1b[24;39m\x1b]8;;\x1b\\")
				i += end
				continue
			}
		}
		if code, end, ok := liveActivityCodeSpan(line, i); ok {
			// Inline spans have no language tag. Detect from their content,
			// retaining the accent for plain text and uncolored tokens.
			highlighted := code
			if !p.LayoutOnly && len(code) <= outputAutoHighlightBytes {
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
		out.WriteByte(line[i])
		i++
	}
	if bold {
		out.WriteString(Undim)
	}
	return out.String()
}

// Local paths, file URLs and HTTP(S) URLs become terminal links. File existence
// and workspace-relative resolution belong to the click handler.
func liveActivityLink(s string) (label, target string, end int, ok bool) {
	close := strings.Index(s, "](")
	if close < 2 || s[0] != '[' {
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
		if line, err := strconv.Atoi(path[colon+1:]); err == nil && line > 0 {
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

// markdown renders authored text: fenced programs are highlighted on a
// fill, list items hang, and tables lay out within the available width.
func (p *Painter) Markdown(text string, width int) []string {
	return p.markdown(text, width, false)
}

// Reasoning keeps source indentation on soft-wrapped continuation rows too.
// Ordinary authored message layout is unchanged.
func (p *Painter) markdown(text string, width int, reasoning bool) []string {
	copying := p.CopySource && !p.LayoutOnly
	identity := uint64(0)
	if copying {
		identity = copyID(text, p.CopyScope)
	}
	annotate := func(rows []string, source, prefix string, gutter, index int) []string {
		if !copying {
			return rows
		}
		f := copyInline(source)
		f.ID = copyID(strconv.Itoa(index), identity)
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
		line := livediff.Safe(raw, false)
		if fence != "" {
			if line != fence {
				program = append(program, raw)
				continue
			}
			body := strings.Join(program, "\n")
			var diagram []string
			if strings.EqualFold(lang, "mermaid") {
				diagram, _ = mermaid.Render(livediff.Safe(body, false), width)
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
		if delimiter, ok := FenceDelimiter(line); ok {
			fence, lang = delimiter, strings.TrimSpace(line[len(delimiter):])
			if copying {
				fenceIdentity = copyID(strconv.Itoa(i), identity)
			}
			continue
		}
		if table, consumed := parseMarkdownTable(source[i:]); consumed > 0 {
			if copying {
				table.copyID = copyID(strings.Join(source[i:i+consumed], "\n"), identity+uint64(i))
			}
			lines = append(lines, p.markdownTable(table, max(1, width))...)
			i += consumed - 1
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		switch {
		case strings.HasPrefix(trimmed, "#"):
			lines = append(lines, annotate(Wrap("\x1b[1m"+p.Inline(strings.TrimLeft(trimmed, "# "))+Undim, width, false), strings.TrimLeft(trimmed, "# "), trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, "# "))], 0, i)...)
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			lines = append(lines, annotateHang(liveActivityHang(indent+Dim+"•"+Undim+" ", p.Inline(trimmed[2:]), width), trimmed[2:], indent+trimmed[:2], ansi.StringWidth(indent)+2, i)...)
		default:
			if reasoning && indent != "" {
				lines = append(lines, annotateHang(liveActivityHang(indent, p.Inline(trimmed), width), trimmed, indent, ansi.StringWidth(indent), i)...)
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

// quote keeps a visible bar on every wrapped row and renders the quoted
// Markdown normally, including lists, nested quotes, and fenced code. The bar
// is heavier than feed gutters so a quote reads as quoted, not as a thread.
func (p *Painter) Quote(text string, width int) []string {
	rows := p.Markdown(text, max(1, width-2))
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

// program keeps operation source under a gutter beside its verb.
func (p *Painter) program(lang, source string, width int) []string {
	var lines []string
	for _, line := range p.Highlight(lang, source) {
		for _, part := range Wrap(line, width-2, true) {
			lines = append(lines, Dim+"│"+Undim+" "+part)
		}
	}
	return lines
}

// fenced sets authored code on a padded fill so it cannot be mistaken for a
// quote. An undetected theme (as behind mosh, which answers no OSC 11 query)
// gets a self-contained dark block: highlighting already assumes dark, and an
// explicit foreground keeps plain text readable on either terminal theme.
func (p *Painter) fenced(lang, source string, width int, identity uint64) []string {
	fill, ink := p.codeBackground(), ""
	if fill == "" {
		fill, ink = "\x1b[48;2;32;35;40m", "\x1b[38;2;230;237;243m"
	}
	var lines []string
	var sources []string
	if p.CopySource && !p.LayoutOnly {
		sources = strings.Split(source, "\n")
	}
	for index, line := range p.Highlight(lang, livediff.Safe(source, false)) {
		parts := Wrap(line, width-2, true)
		if len(sources) > 0 {
			parts = p.CopyWrapped(parts, copyCode(sources[index], copyID(strconv.Itoa(index), identity)), 0)
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

// codeBackground lifts the reported terminal background slightly toward its
// foreground side, falling back to fixed fills for a theme known only by name.
func (p *Painter) codeBackground() string {
	if p.Colors.HasBackground {
		bg, to := p.Colors.Background, uint8(255)
		if p.Theme == livediff.LightTheme {
			to = 0
		}
		channel := func(from uint8) int { return int(float64(from) + (float64(to)-float64(from))*.07 + .5) }
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", channel(bg.R), channel(bg.G), channel(bg.B))
	}
	switch p.Theme {
	case livediff.LightTheme:
		return "\x1b[48;2;242;243;245m"
	case livediff.DarkTheme:
		return "\x1b[48;2;32;35;40m"
	}
	return ""
}

func liveActivityIndent(lines []string, prefix string) []string {
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return lines
}

// Block renders one parsed block within width columns.
func (p *Painter) Block(block Block, width int) []string {
	width = max(8, width)
	lines := p.blockRows(block, width)
	if outcome := approvalLabel(block.Approval); outcome != "" && len(lines) > 0 {
		suffix := Dim + " · " + Undim + outcome
		if ansi.StringWidth(lines[0])+ansi.StringWidth(suffix) <= width {
			lines[0] += suffix
		} else {
			lines = slices.Insert(lines, 1, liveActivityHang(strings.Repeat(" ", block.cell(RowVerb(block))), outcome, width)...)
		}
	}
	if block.ShowWorkdir && block.Workdir != "" && block.Verb != "Run" && len(lines) > 0 {
		// Run rows carry it in their label, ahead of the exit and elapsed time.
		if suffix := " " + workdirLabel(block.Workdir); ansi.StringWidth(lines[0])+ansi.StringWidth(suffix) <= width {
			lines[0] += suffix
		} else {
			lines = slices.Insert(lines, 1, liveActivityHang(strings.Repeat(" ", block.cell(RowVerb(block))), workdirLabel(block.Workdir), width)...)
		}
	}
	if block.Verb != "Run" && !block.Skipped && len(lines) > 0 {
		if elapsed := RunElapsed(block, p.now()); elapsed != "" {
			suffix := Dim + " · " + elapsed + Undim
			if ansi.StringWidth(lines[0])+ansi.StringWidth(suffix) <= width {
				lines[0] += suffix
			} else {
				lines = append(lines, strings.Repeat(" ", block.cell(RowVerb(block)))+Dim+"· "+elapsed+Undim)
			}
		}
	}
	if trailer := segmentTrailer(block); trailer != "" && len(lines) > 0 {
		if last := len(lines) - 1; ansi.StringWidth(lines[last])+1+ansi.StringWidth(trailer) <= width {
			lines[last] += " " + trailer
		} else {
			lines = append(lines, strings.Repeat(" ", block.cell(RowVerb(block)))+trailer)
		}
	}
	if block.Skipped {
		for i, line := range lines {
			lines[i] = Dim + ansi.Strip(line) + Reset
		}
	}
	if !block.Collapsed {
		block.Tail = p.tailColors(block)
	}
	return outputRows(block, lines, width)
}

// approvalLabel keeps the decision separate from whether a command ran or
// succeeded. Withdrawal and external resolution do not imply a decision.
func approvalLabel(outcome string) string {
	switch {
	case strings.HasPrefix(outcome, "Approved"), strings.HasPrefix(outcome, "Granted"), strings.HasPrefix(outcome, "Allowed"):
		return Green + "Approved" + Reset
	case strings.HasPrefix(outcome, "Denied"), strings.HasPrefix(outcome, "Declined"), strings.HasPrefix(outcome, "Blocked"):
		return Red + "Denied" + Reset
	case outcome != "":
		return Dim + livediff.Safe(outcome, false) + Undim
	}
	return ""
}

// workdirLabel names the directory a command ran in when it is not the
// workspace, so its relative paths do not read as workspace paths.
func workdirLabel(workdir string) string {
	return Dim + "· in " + Undim + Path(workdir)
}

// segmentTrailer names a tracked segment's outcome where its row would not:
// a list that never reached it, or the exit of an operation whose row shows
// no exit, such as a failed read.
func segmentTrailer(block Block) string {
	switch {
	case block.Skipped && EditStatus(block) != "skipped":
		return "· skipped"
	case !block.Segment || block.ExitCode == 0 || block.GroupHeader != "" || block.Kind != "op" && block.Kind != "reads" || block.VCS():
		return ""
	case block.Kind == "op" && (block.Verb == "Run" || block.Verb == "Skill" || block.Verb == "Capture" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, block.Verb)):
		return "" // The row already shows the exit.
	}
	return Dim + "· " + Undim + exitText(block.ExitCode)
}

func (p *Painter) blockRows(block Block, width int) []string {
	if block.GroupHeader != "" && block.Kind == "op" {
		return p.editGroupRow(block, width)
	}
	switch block.Kind {
	case "progress":
		text := block.progressText(true)
		if block.Label != "" && block.Hovered {
			text = Underline(text)
		}
		return Wrap(Dim+"• "+text+Reset, width, false)
	case "summary":
		// Public reasoning shares one streaming block across providers.
		if block.Label == "" {
			block.Label = ReasoningSections(block.Body)[0].Label
		}
		body := ReasoningSummaryBody(block.Body)
		if body == "" {
			// A request's thinking block before its first delta.
			if block.Live && strings.TrimSpace(block.Body) == "" {
				header, _ := p.thinkingHeader(block, width-2)
				return []string{Dim + "• " + header + Undim}
			}
			return nil
		}
		header, headingElided := p.thinkingHeader(block, width-2)
		rows, short, titleOnly := p.reasoningContent(block, width)
		if short {
			// Short summaries stay visible, including heading-only summaries,
			// both while streaming and after completion.
			text := rows[0]
			if !block.Live && block.Elapsed != "" {
				text = reasoningFor(text, block.Elapsed)
			}
			rows = liveActivityHang("• ", text, width)
			for i, row := range rows {
				rows[i] = reasoningRow(row)
			}
			return rows
		}
		if block.Collapsed {
			if block.Hovered {
				header = Underline(header)
			}
			return []string{Dim + "• " + header + Undim}
		}
		if block.Live {
			var hidden int
			if rows, hidden = TailRows(rows, ThinkingTailRows); hidden > 0 {
				suffix := " · " + Elision{Hidden: hidden, Form: ElisionSuffix, Hovered: block.Hovered}.String()
				suffix = ansi.Truncate(suffix, max(0, width-3), "…")
				header, headingElided = p.thinkingHeader(block, width-2-ansi.StringWidth(suffix))
				header += suffix + Dim
			}
		}
		if block.Hovered && headingElided && !titleOnly {
			header = Underline(header)
		}
		for i, row := range rows {
			rows[i] = reasoningRow("  " + row)
		}
		return append([]string{Dim + "• " + header + Undim}, rows...)
	case "final":
		if block.Journal != nil {
			return p.Journal(block.Journal, width, true)
		}
		return append([]string{Green + "✓ Final answer" + Reset}, liveActivityIndent(p.Markdown(block.Body, width-2), "  ")...)
	case "reads":
		lead, count := block.lead(VerbColor(block.Verb), block.Verb), ResultCount(block.Results)+readLines(block)
		indent := ansi.StringWidth(lead)
		literal := block.Verb == "Search" || block.Verb == "Skill"
		// fit shortens a path that cannot share its row with its ranges, gap
		// cells after it, and, on the last row, the count after them.
		fit := func(read Read, gap int, last bool) string {
			if literal {
				return read.Path
			}
			room := width - indent
			if len(read.Ranges) > 0 {
				room -= ansi.StringWidth(lineRanges(read.Ranges)) + gap
			}
			if last {
				room -= ansi.StringWidth(count)
			}
			return fitPath(read.Path, room-ansi.StringWidth(lineSuffix(read.Lines, false)))
		}
		item := func(path string, read Read) string {
			item := Path(path)
			if literal {
				item = p.code(block.Verb, path)
			}
			if len(read.Ranges) > 0 {
				item += " " + Dim + lineRanges(read.Ranges) + Undim
			}
			return item + lineSuffix(read.Lines, block.Hovered)
		}
		var items []string
		for _, read := range block.Reads {
			items = append(items, item(read.Path, read))
		}
		joined := strings.Join(items, Dim+" · "+Undim) + count
		if indent+ansi.StringWidth(joined) <= width || indent > width/2 {
			return liveActivityHang(lead, joined, width)
		}
		if len(items) < 2 {
			return liveActivityHang(lead, item(fit(block.Reads[0], 1, true), block.Reads[0])+count, width)
		}
		// Items that do not fit on one row take one row each, rather than
		// leaving separators dangling at wrapped row ends. Their ranges share
		// a column when it fits.
		paths := make([]string, len(block.Reads))
		column := 0
		for i, read := range block.Reads {
			paths[i] = fit(read, 2, i == len(block.Reads)-1)
			items[i] = item(paths[i], read)
			column = max(column, ansi.StringWidth(paths[i]))
		}
		for i, read := range block.Reads {
			if len(read.Ranges) > 0 && !literal && indent+column+2+ansi.StringWidth(lineRanges(read.Ranges)+lineSuffix(read.Lines, false)) <= width {
				items[i] = Path(paths[i]) + strings.Repeat(" ", column-ansi.StringWidth(paths[i])+2) + Dim + lineRanges(read.Ranges) + Undim + lineSuffix(read.Lines, block.Hovered)
			}
		}
		items[len(items)-1] += count
		var lines []string
		for i, item := range items {
			if i > 0 {
				lead = strings.Repeat(" ", indent)
			}
			lines = append(lines, liveActivityHang(lead, item, width)...)
		}
		return lines
	case "op":
		if len(block.Questions) > 0 {
			return p.questionRows(block, width)
		}
		if block.VCS() {
			return p.vcsRow(block, width)
		}
		if block.Verb == "Run" {
			return p.ranRow(block, width)
		}
		label := p.Label(block.Verb, block.Label) + ResultCount(block.Results)
		if block.Path != "" {
			path := Path(livediff.Safe(block.Path, false))
			if label != "" {
				path += Dim + " · " + Undim + label
			}
			label = path
		}
		if row, ok := p.editRow(block, width, block.cell(block.Verb)); ok {
			label = row
		}
		code, body := block.Code, block.Body
		exit := ""
		if block.ExitCode != 0 && (block.Verb == "Run" || block.Verb == "Skill" || block.Verb == "Capture" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, block.Verb)) {
			exit = Red + fmt.Sprintf("(exit %d)", block.ExitCode) + Reset
		}
		// Keep exec source below its long heading, regardless of source line count.
		// Other single-line programs or arguments read best on the operation row.
		if code != "" && !strings.Contains(code, "\n") && (label == "" || !block.Fenced) && !(block.Fenced && block.Verb == "Run JavaScript") {
			inline := p.code(block.Verb, code)
			if block.Fenced {
				inline = strings.Join(p.Highlight(block.Lang, code), " ")
			}
			label, code = strings.TrimSpace(label+" "+inline), ""
		}
		lines := liveActivityHang(block.lead(VerbColor(block.Verb), block.Verb), label, width)
		if code != "" {
			lang := block.Lang
			if !block.Fenced && (block.Verb == "MCP" || strings.HasPrefix(block.Verb, "Tool")) {
				lang = "json"
			}
			lines = append(lines, clipSource(liveActivityIndent(p.program(lang, sourceTabs(code), width-2), "  "), block, "  "+Dim+"│"+Undim+" ")...)
		}
		if body != "" {
			lines = append(lines, liveActivityIndent(p.Markdown(body, width-2), "  ")...)
		}
		if exit != "" {
			last := len(lines) - 1
			if ansi.StringWidth(lines[last])+1+ansi.StringWidth(exit) <= width {
				lines[last] += " " + exit
			} else {
				lines = append(lines, strings.Repeat(" ", block.cell(block.Verb))+exit)
			}
		}
		return lines
	case "message":
		lines := []string{ansi.Truncate(p.messageDirection(block), width, "…")}
		return append(lines, p.Markdown(block.Body, width)...)
	case "start":
		head := Green + "\x1b[1m▶ Started" + Reset
		lines := liveActivityHang(head+" · ", p.Inline(block.Label), width)
		if block.Body != "" {
			lines = append(lines, p.Markdown(block.Body, width)...)
		}
		return lines
	case "compaction":
		return []string{Amber + "◉ Context compacted" + Reset}
	case "batch":
		return p.batchRow(block, width)
	case "filter":
		return filterRows(block.Body, width, min(block.cell("Run"), width/2))
	case "error":
		return liveActivityIndent(ErrorRows(block, width-2), Red+"✗"+Reset+" ")
	}
	return p.Markdown(block.Body, width)
}

// filterRows aligns an auxiliary note with its command text.
func filterRows(body string, width, indent int) []string {
	rows := Wrap(body, width-indent, true)
	for i := range rows {
		rows[i] = strings.Repeat(" ", indent) + Dim + rows[i] + Reset
	}
	return rows
}

func exitText(code int) string {
	return Red + fmt.Sprintf("exit %d", code) + Reset
}

// editGroupRow lays out one file row of an edit group. Rows share one verb
// cell: the first row names the group's verb, source, and any unconfirmed
// outcome, and later rows name only a verb that differs from Edit.
func (p *Painter) editGroupRow(block Block, width int) []string {
	verb, status, start := EditVerb(block), EditStatus(block), block.GroupStart
	color := Green
	switch status {
	case "failed", "declined":
		color = Red
	case "requested", "pending":
		color = Amber
	}
	cell := max(block.cell("Edit"), block.VerbAlign+1)
	lead := strings.Repeat(" ", cell)
	if start || block.Verb != "Edit" {
		lead = color + "\x1b[1m" + verb + Reset + strings.Repeat(" ", cell-ansi.StringWidth(verb))
	}
	block.Label = editLabel(block)
	if status != "" {
		// The first row names the outcome that every row's tail repeats.
		block.Label = strings.TrimSuffix(block.Label, " · "+status)
	}
	row, ok := p.editRow(block, width, cell)
	if !ok {
		row = p.Label(block.Verb, block.Label)
	}
	var trailer []string
	if start && !block.DirectoryDeletion() {
		trailer = p.groupSource(block)
		if status != "" {
			trailer = append(trailer, Dim+"· "+Undim+color+status+Reset)
		}
	}
	if block.ExitCode != 0 {
		trailer = append(trailer, Dim+"· "+Undim+exitText(block.ExitCode))
	}
	rows := liveActivityHang(lead, row, width)
	// The trailer follows the file when it fits, and otherwise takes its own row.
	if last, text := len(rows)-1, strings.Join(trailer, " "); text == "" || ansi.StringWidth(rows[last])+1+ansi.StringWidth(text) <= width {
		rows[last] = strings.TrimSuffix(rows[last]+" "+text, " ")
	} else {
		rows = append(rows, liveActivityHang(strings.Repeat(" ", cell), text, width)...)
	}
	return rows
}

// groupSource names edit sources other than stock apply_patch, each
// highlighted as its own command, and the invocations a group spans.
func (p *Painter) groupSource(block Block) []string {
	var parts []string
	if source := strings.TrimSuffix(block.EditSource, requestedEdit); source != "" && source != "apply_patch" {
		var sources []string
		for part := range strings.SplitSeq(source, ", ") {
			sources = append(sources, strings.Join(p.Highlight("bash", part), " ")+Reset)
		}
		parts = append(parts, Dim+"via "+Undim+strings.Join(sources, Dim+", "+Undim))
	}
	if block.GroupCount > 1 {
		parts = append(parts, Dim+fmt.Sprintf("×%d", block.GroupCount)+Undim)
	}
	return parts
}

// editRow lays out a file row with its counts in the group's shared column.
// Zero counts are omitted.
func (p *Painter) editRow(block Block, width, lead int) (string, bool) {
	if !slices.Contains([]string{"Create", "Edit", "Delete"}, block.Verb) {
		return "", false
	}
	path, added, removed, tail, ok := EditStat(block.Label)
	if !ok {
		return "", false
	}
	plain := editCounts(added, removed)
	stats := countText(plain)
	room := width - lead - ansi.StringWidth(tail)
	if plain != "" {
		room -= ansi.StringWidth(plain) + 1
	}
	// Every row of a group decides its count column alike: the widest path,
	// narrowed so the group's widest counts still fit, eliding longer paths.
	column := min(block.PathAlign, width-lead-2-block.StatAlign)
	if plain == "" || column < min(16, block.PathAlign) {
		column = 0
	}
	if column > 0 {
		room = min(room, column)
	}
	path = fitPath(path, room)
	row := Path(path)
	if plain != "" {
		pad := 1
		if column > 0 && ansi.StringWidth(path) <= column {
			pad = column - ansi.StringWidth(path) + 2
		}
		row += strings.Repeat(" ", pad) + stats
		if bar := statBar(added, removed, block.StatScale); bar != "" {
			if gap := max(1, block.StatAlign-ansi.StringWidth(plain)+1); lead+ansi.StringWidth(row)+gap+statBarCells <= width {
				row += strings.Repeat(" ", gap) + bar
			}
		}
	}
	if tail != "" {
		row += p.Label(block.Verb, tail)
	}
	return row, true
}

const statBarCells = 8

// statBar scales a row's changed lines against the largest row in its group.
func statBar(added, removed, scale int) string {
	total := added + removed
	if scale <= 0 || total <= 0 {
		return ""
	}
	filled := min(statBarCells, max(1, (total*statBarCells+scale/2)/scale))
	green := (added*filled + total/2) / total
	if added > 0 {
		green = max(1, green)
	}
	if removed > 0 && filled > 1 {
		green = min(green, filled-1)
	}
	return Green + strings.Repeat("━", green) + Red + strings.Repeat("━", filled-green) + "\x1b[39m" +
		Dim + strings.Repeat("━", statBarCells-filled) + Undim
}

// fitPath shortens a path wider than width from its start, dropping whole
// leading directories so its nearest directories and file name remain.
func fitPath(path string, width int) string {
	// Below this, eliding leaves too little of the name; rows wrap instead.
	if width < 16 {
		return path
	}
	return TruncatePath(path, width)
}

// TruncatePath uses the feed's path elision in single-line controls, including
// widths where feed rows would wrap instead. It keeps nearby directories and
// the filename, or the filename's start and extension when it cannot fit.
func TruncatePath(path string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(path) <= width {
		return path
	}
	i := strings.LastIndex(strings.TrimSuffix(path, "/"), "/")
	base := path[i+1:]
	if i >= 0 && ansi.StringWidth(base)+2 <= width {
		// The last separator always fits, so this finds a cut.
		for cut := 1; cut <= i; cut++ {
			if path[cut] == '/' && 1+ansi.StringWidth(path[cut:]) <= width {
				return "…" + path[cut:]
			}
		}
	}
	// The name alone is too wide: keep its start and its extension.
	if i >= 0 && width > 4 {
		return "…/" + elideMiddle(base, width-2)
	}
	return elideMiddle(path, width)
}

func elideMiddle(text string, width int) string {
	total := ansi.StringWidth(text)
	if total <= width {
		return text
	}
	tail := (width - 1) / 2
	return ansi.Truncate(text, width-1-tail, "") + "…" + ansi.TruncateLeft(text, total-tail, "")
}

// lineRanges shows read spans as line numbers: 25:46 reads as L25–46.
func lineRanges(ranges []string) string {
	shown := make([]string, len(ranges))
	for i, span := range ranges {
		from, to, ok := strings.Cut(span, ":")
		switch {
		case !ok:
			shown[i] = span
		case from == to:
			shown[i] = "L" + from
		default:
			shown[i] = "L" + from + "–" + to
		}
	}
	return strings.Join(shown, ", ")
}

// ranRow lays out one command after Running or Ran: a one-line command beside
// the verb, or a program beside its gutter, then any failure's exit and the
// live or failed output tail.
func (p *Painter) ranRow(block Block, width int) []string {
	code, label := sourceTabs(block.Code), strings.TrimSpace(p.Label(block.Verb, block.Label)+ResultCount(block.Results))
	if code == "" {
		if span, end, ok := liveActivityCodeSpan(block.Label, 0); ok && end == len(block.Label) {
			code, label = span, ""
		}
	}
	if block.ShowWorkdir && block.Workdir != "" {
		label = strings.TrimSpace(label + " " + workdirLabel(block.Workdir))
	}
	color, exit := VerbColor("Run"), ""
	if block.ExitCode != 0 {
		color, exit = Red, Dim+"· "+Undim+exitText(block.ExitCode)
	}
	verb := RowVerb(block)
	lead, indent := block.lead(color, verb), block.cell(verb)
	padding := strings.Repeat(" ", indent)
	// suffix follows the last row when it fits, and otherwise takes its own.
	suffix := func(lines []string, text string) []string {
		if last := len(lines) - 1; ansi.StringWidth(lines[last])+1+ansi.StringWidth(text) <= width {
			lines[last] += " " + text
			return lines
		}
		return append(lines, liveActivityHang(padding, text, width)...)
	}
	var lines []string
	switch {
	case code != "" && !strings.Contains(code, "\n"):
		lang := "bash"
		if block.Fenced && block.Lang != "" {
			lang = block.Lang
		}
		lines = p.command(lead, lang, code, width, block)
		if label != "" {
			lines = suffix(lines, label)
		}
	case code != "":
		program := clipSource(liveActivityIndent(p.program(block.Lang, code, width-indent), padding), block, padding+Dim+"│"+Undim+" ")
		if label != "" {
			lines = liveActivityHang(lead, label, width)
		} else {
			program[0] = lead + strings.TrimPrefix(program[0], padding)
		}
		lines = append(lines, program...)
	default:
		lines = liveActivityHang(lead, label, width)
	}
	switch {
	case exit != "" && strings.Contains(code, "\n"):
		// Beside a program's last row, the exit would read as source.
		lines = append(lines, padding+exit)
	case exit != "":
		lines = suffix(lines, exit)
	}
	if elapsed := RunElapsed(block, p.now()); elapsed != "" {
		if strings.Contains(code, "\n") {
			lines = append(lines, padding+Dim+"· "+elapsed+Undim)
		} else {
			lines = suffix(lines, Dim+"· "+elapsed+Undim)
		}
	}
	if block.Body != "" {
		lines = append(lines, liveActivityIndent(p.Markdown(block.Body, width-indent-2), padding+"  ")...)
	}
	return lines
}

// sourceTabs expands tabs as livediff.Safe does; a raw tab has no cell width,
// so wrapping would misjudge its row.
func sourceTabs(code string) string {
	return strings.ReplaceAll(code, "\t", "    ")
}

// clipSource keeps a command or program preview within block.SourceRows,
// counting wrapped rows, so a one-line command wider than many rows is bounded
// too. The last kept row counts the rows left out, under lead.
func clipSource(rows []string, block Block, lead string) []string {
	if block.SourceRows <= 0 || len(rows) <= block.SourceRows {
		return rows
	}
	keep := max(1, block.SourceRows-1)
	return append(rows[:keep:keep], lead+Elision{Hidden: len(rows) - keep, Hovered: block.Hovered}.String())
}

// readLines counts collapsed read output after the target it came from: a
// file's content needs no row of its own to say how much there is.
func readLines(block Block) string {
	if !block.Collapsed || !block.ReadOutput() || len(block.Tail) == 0 {
		return ""
	}
	return lineSuffix(block.TailOmitted+len(block.Tail), block.Hovered)
}

// lineSuffix counts n lines of collapsed content, underlined under the
// pointer that opens it.
func lineSuffix(n int, hovered bool) string {
	if n <= 0 {
		return ""
	}
	return " " + Elision{Hidden: n, Form: ElisionContent, Hovered: hovered}.String()
}

// outputRows attaches the invocation's output to its final operation. Open
// output counts its earlier lines in the verb column of its first row.
func outputRows(block Block, lines []string, width int) []string {
	indent := block.cell(RowVerb(block))
	padding := strings.Repeat(" ", indent)
	// A VCS row's changes are its result, not output to collapse.
	if len(block.Changes) > 0 && (!block.Collapsed || block.VCS()) {
		return append(lines, changeRows(block.Changes, padding, width-indent)...)
	}
	tail, omitted := block.Tail, block.TailOmitted
	if block.TailRows > 0 && len(tail) > block.TailRows {
		omitted += len(tail) - block.TailRows
		tail = tail[len(tail)-block.TailRows:]
	}
	if suffix := readLines(block); suffix != "" && len(lines) > 0 {
		last := &lines[len(lines)-1]
		if block.Kind == "reads" {
			return lines // Its layout placed the count.
		}
		if ansi.StringWidth(*last)+ansi.StringWidth(suffix) <= width {
			*last += suffix
			return lines
		}
	}
	// Output uses a dashed gutter, distinct from the program gutter above it.
	if block.Collapsed && len(tail) > 0 {
		hint := Elision{Hidden: omitted + len(tail), Hovered: block.Hovered}
		return append(lines, padding+ansi.Truncate(Dim+"┆ "+Undim+hint.String(), width-indent, "…"))
	}
	for i, line := range tail {
		lead := padding
		if count := (Elision{Hidden: omitted, Form: ElisionLead, Hovered: block.Hovered}); i == 0 && omitted > 0 {
			if cells := ansi.StringWidth(count.Text()); cells < indent {
				lead = count.String() + padding[cells:]
			} else {
				// A count wider than the verb column keeps its own row.
				count.Form = ElisionEarlier
				lines = append(lines, padding+ansi.Truncate(Dim+"┆ "+Undim+count.String(), width-indent, "…"))
			}
		}
		lines = append(lines, lead+ansi.Truncate(Dim+"┆"+Undim+" "+line, width-indent, "…"))
	}
	return lines
}

// command places a one-line command after lead. A statement after a
// separator starts a row under the first; other continuations sit two
// columns deeper.
func (p *Painter) command(lead, lang, code string, width int, block Block) []string {
	styled := strings.Join(p.Highlight(lang, code), " ")
	indent := ansi.StringWidth(lead)
	padding := strings.Repeat(" ", indent)
	if indent > width/2 || ansi.Strip(styled) != code {
		return clipSource(liveActivityHang(lead, styled, width), block, padding)
	}
	var lines []string
	wrapped, hidden := shellWrap(styled, code, width-indent, lang == "bash", block.SourceRows)
	for i, row := range wrapped {
		switch {
		case i == 0:
			lines = append(lines, lead+row.text)
		case row.deeper:
			lines = append(lines, strings.Repeat(" ", indent+2)+row.text)
		default:
			lines = append(lines, strings.Repeat(" ", indent)+row.text)
		}
	}
	if hidden > 0 {
		keep := max(1, block.SourceRows-1)
		hidden += len(lines) - keep
		lines = append(lines[:keep:keep], padding+Elision{Hidden: hidden, Hovered: block.Hovered}.String())
	}
	return lines
}

type wrapRow struct {
	text   string
	deeper bool // Continues a statement rather than starting one.
}

// shellWrap breaks a styled one-line command at unquoted blanks of its plain
// text. When the command does not fit, each top-level statement after ;, &&,
// or || starts a row. Other breaks inside a statement end with a line
// continuation, or follow a pipe, so each row reads as the same command; a
// word wider than a row is cut without one. A positive limit materializes only
// that many rows, returning the exact count of the rest without ANSI slicing.
func shellWrap(styled, plain string, width int, shell bool, limit int) ([]wrapRow, int) {
	type blank struct {
		from, to  int  // Cell offsets of a blank run.
		statement bool // Follows a top-level statement separator.
		pipe      bool
	}
	var blanks []blank
	quote, escaped := rune(0), false
	cells, depth := 0, 0
	var previous [2]rune // The two runes before the current one.
	for _, r := range plain {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '(':
			depth++
		case r == ')':
			depth = max(0, depth-1)
		case r == ' ' || r == '\t':
			if n := len(blanks); n > 0 && blanks[n-1].to == cells {
				blanks[n-1].to++
				break
			}
			last, before := previous[1], previous[0]
			top := depth == 0 && before != '\\'
			blanks = append(blanks, blank{from: cells, to: cells + 1,
				statement: top && (last == ';' || last == '&' && before == '&' || last == '|' && before == '|'),
				pipe:      top && last == '|' && before != '|'})
		}
		previous[0], previous[1] = previous[1], r
		cells += ansi.StringWidth(string(r))
	}
	continuation := 0
	if shell {
		continuation = 2
	}
	total := ansi.StringWidth(plain)
	if total <= width {
		return []wrapRow{{styled, false}}, 0
	}
	var rows []wrapRow
	hidden := 0
	add := func(start, end int, deeper, continuation bool) {
		if limit > 0 && len(rows) >= max(1, limit) {
			hidden++
			return
		}
		text := ansi.Cut(styled, start, end) + Reset
		if continuation {
			text += Dim + " \\" + Undim
		}
		rows = append(rows, wrapRow{text, deeper})
	}
	start, firstBlank, deeper := 0, 0, false
	for {
		avail := max(1, width)
		if deeper {
			avail = max(1, width-2)
		}
		for firstBlank < len(blanks) && blanks[firstBlank].from <= start {
			firstBlank++
		}
		chosen := -1
		for i := firstBlank; i < len(blanks); i++ {
			b := blanks[i]
			if b.from-start > avail {
				break
			}
			if b.statement {
				chosen = i
				break
			}
		}
		if chosen < 0 && total-start <= avail {
			add(start, total, deeper, false)
			return rows, hidden
		}
		if chosen < 0 {
			for i := firstBlank; i < len(blanks); i++ {
				b := blanks[i]
				if b.from-start > avail {
					break
				}
				cost := continuation
				if b.pipe || b.statement {
					cost = 0
				}
				if b.from-start+cost <= avail {
					chosen = i
				}
			}
		}
		if chosen < 0 {
			end := start + avail
			add(start, end, deeper, false)
			start, deeper = end, true
			continue
		}
		b := blanks[chosen]
		add(start, b.from, deeper, !b.pipe && !b.statement && shell)
		start, deeper = b.to, !b.statement
	}
}

// event renders a block for the native Activity log: a short label row, then
// the body flush with the gutter so narrow panes keep their full width.
// Operations keep their ordinary one-row-per-operation layout.
func (p *Painter) Event(block Block, width int) []string {
	width = max(8, width)
	// A flashed entry highlights its text, never its label row.
	label := func(head string, body []string) []string {
		return append([]string{ansi.Truncate(head, width, "…")}, p.Flash(block, body)...)
	}
	done := Green + "✓" + Reset + Dim + " answer" + Undim
	switch block.Kind {
	case "message":
		return label(p.Route(block), p.Markdown(block.Body, width))
	case "start":
		head := Green + "▶" + Reset + Dim + " started"
		if model := strings.TrimSpace(ansi.Strip(p.Inline(block.Label))); model != "" {
			head += " · " + model
		}
		return label(head+Undim, p.Markdown(block.Body, width))
	case "final":
		// The answer is a card, so it stands apart from the work before it.
		inner := width - 4
		if inner < 8 {
			inner = width
		}
		title := done
		var rows []string
		if block.Journal == nil {
			rows = p.Markdown(block.Body, inner)
		} else {
			var answers []Answer
			for _, group := range block.Journal.Groups {
				answers = append(answers, group.Answers...)
			}
			switch len(answers) {
			case 0:
				rows = []string{Dim + "No journal entries" + Undim}
			case 1:
				rows = p.Markdown(answers[0].Text, inner)
			default:
				title += Dim + fmt.Sprintf(" · %d", len(answers)) + Undim
				for _, answer := range answers {
					rows = append(rows, liveActivityHang(Dim+"•"+Undim+" ", strings.Join(p.Markdown(answer.Text, inner-2), "\n"), inner)...)
				}
			}
			if totals := block.Journal.totals(); totals != "" {
				// The title keeps the totals visible when the card is clipped.
				title += Dim + " · " + Undim + totals
			}
			tail := *block.Journal
			tail.Groups, tail.empty = nil, false
			rows = append(rows, p.Journal(&tail, inner, false)...)
		}
		if width < 12 {
			return label(title, rows)
		}
		return liveActivityCard(title, p.Flash(block, rows), width, block.Flash)
	case "text":
		return p.Flash(block, p.Markdown(block.Body, width))
	}
	return p.Flash(block, p.Block(block, width))
}

// Flash highlights the text of a flashed block's rows, leaving padding and
// frames plain.
func (p *Painter) Flash(block Block, rows []string) []string {
	if !block.Flash {
		return rows
	}
	fill := p.Theme.SelectionBackground()
	flashed := make([]string, len(rows))
	for i, row := range rows {
		flashed[i] = fill + strings.ReplaceAll(row, Reset, Reset+fill) + "\x1b[49m"
	}
	return flashed
}

// journal lays out a final journal result: a heading with its answer and
// change totals, each question with its answers, then recorded changes. The
// agent heading already names the author. The shared feed keeps questions to
// one row so clipping reaches the answers.
func (p *Painter) Journal(journal *Journal, width int, heading bool) []string {
	answers := 0
	for _, group := range journal.Groups {
		answers += len(group.Answers)
	}
	head := Green + "✓ Final answer" + Reset
	var facts []string
	if answers > 1 {
		facts = append(facts, fmt.Sprintf("%d answers", answers))
	}
	if totals := journal.totals(); totals != "" {
		facts = append(facts, totals)
	}
	if len(facts) > 0 {
		head += Dim + " · " + strings.Join(facts, " · ") + Undim
	}
	var lines []string
	if heading {
		lines = append(lines, ansi.Truncate(head, width, "…"))
	}
	if journal.empty {
		lines = append(lines, "  "+Dim+"No journal entries"+Undim)
	}
	hang := func(lead string, body []string) {
		for i, line := range body {
			if i == 0 {
				lines = append(lines, "  "+lead+line)
			} else {
				lines = append(lines, "    "+line)
			}
		}
		if len(body) == 0 {
			lines = append(lines, "  "+strings.TrimRight(lead, " "))
		}
	}
	for _, group := range journal.Groups {
		if group.Question != "" {
			// The assignment usually sits just above, so one dim line suffices.
			flat := strings.Join(strings.Fields(group.Question), " ")
			lines = append(lines, "  "+Dim+"↩ "+ansi.Truncate(ansi.Strip(p.Inline(flat)), width-4, "…")+Undim)
		}
		for _, answer := range group.Answers {
			lead := "  "
			if group.Question == "" || answers > 1 {
				lead = Dim + "•" + Undim + " "
			}
			body := p.Markdown(answer.Text, width-4)
			if answers > 1 {
				hang(lead, []string{Dim + answer.ID + Undim})
				lines = append(lines, liveActivityIndent(body, "    ")...)
				continue
			}
			hang(lead, body)
		}
	}
	if journal.clipped {
		lines = append(lines, "  "+Dim+"… full answer in Codex completion"+Undim)
	}
	if len(journal.Stats) > 0 || journal.Changes != "" && len(journal.notes) > 0 {
		lines = append(lines, "", Verb("Changes")+Dim+journal.Changes+Undim)
		statusWidth, addedWidth, removedWidth := 0, 0, 0
		for _, stat := range journal.Stats {
			statusWidth = max(statusWidth, len(stat.Status))
			addedWidth, removedWidth = max(addedWidth, len(stat.Added)), max(removedWidth, len(stat.Removed))
		}
		for _, stat := range journal.Stats {
			counts := fmt.Sprintf("%-*s", statusWidth, stat.Status) + "  " + Green + fmt.Sprintf("%*s", addedWidth+1, "+"+stat.Added) + "\x1b[39m " + Red + fmt.Sprintf("%-*s", removedWidth+1, "-"+stat.Removed) + "\x1b[39m  "
			lines = append(lines, ansi.Truncate(counts+Path(stat.Path), width, "…"))
		}
	}
	// "No recorded changes." is the ordinary read-only outcome, not news.
	for _, note := range journal.notes {
		if note != "No recorded changes." && note != "No recorded file changes." {
			lines = append(lines, Wrap(Dim+note+Undim, width, false)...)
		}
	}
	return lines
}

// totals summarizes a journal's recorded file changes, or "" without any.
func (journal *Journal) totals() string {
	if len(journal.Stats) == 0 {
		return ""
	}
	added, removed := 0, 0
	for _, stat := range journal.Stats {
		a, errA := strconv.Atoi(stat.Added)
		r, errR := strconv.Atoi(stat.Removed)
		if errA == nil && errR == nil {
			added, removed = added+a, removed+r
		}
	}
	files := "1 file"
	if len(journal.Stats) > 1 {
		files = fmt.Sprintf("%d files", len(journal.Stats))
	}
	return files + " " + Green + fmt.Sprintf("+%d", added) + "\x1b[39m " + Red + fmt.Sprintf("-%d", removed) + "\x1b[39m"
}

// Verb pads verbs to a common column so arguments line up.
func Verb(verb string) string {
	if verb == "" {
		return ""
	}
	return VerbColor(verb) + "\x1b[1m" + verb + Reset + strings.Repeat(" ", max(1, 7-ansi.StringWidth(verb)))
}

// cell is the width of a row's verb column: the column its adjacent
// operations share, or Verb's default column.
func (b Block) cell(verb string) int {
	if b.VerbColumn > 0 {
		return max(b.VerbColumn, ansi.StringWidth(verb)+1)
	}
	return ansi.StringWidth(Verb(verb))
}

// lead is a row's bold verb in color, padded to its column.
func (b Block) lead(color, verb string) string {
	return color + "\x1b[1m" + verb + Reset + strings.Repeat(" ", b.cell(verb)-ansi.StringWidth(verb))
}

func SummaryVerb(verb string) string {
	return VerbColor(verb) + verb + Reset + " "
}

// Summary is the roster's one-line view of an agent's current activity.
// Width is the status column's available display width, without a bullet prefix.
func (p *Painter) Summary(blocks []Block, width int) string {
	for len(blocks) > 0 && blocks[len(blocks)-1].Kind == "filter" {
		blocks = blocks[:len(blocks)-1]
	}
	if len(blocks) == 0 {
		return ""
	}
	block := blocks[len(blocks)-1]
	more := ""
	if len(blocks) > 1 {
		more = Dim + " · " + Undim + Elision{Hidden: len(blocks) - 1, Unit: "more", Form: ElisionSuffix}.String()
	}
	firstLine := func(text string) string {
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
		if fence, ok := FenceDelimiter(source[0]); ok && strings.EqualFold(strings.TrimSpace(source[0][len(fence):]), "mermaid") {
			for _, line := range source[1:] {
				if line = strings.TrimSpace(line); line != "" && line != fence {
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
		for _, line := range p.Markdown(text, 1<<16) {
			if plain := strings.TrimSpace(strings.TrimPrefix(ansi.Strip(line), "│")); plain != "" {
				return strings.TrimSpace(strings.TrimPrefix(plain, "• "))
			}
		}
		return ""
	}
	switch block.Kind {
	case "progress":
		return Dim + block.progressText(true) + Reset
	case "summary":
		var headers []string
		for i := len(blocks) - 1; i >= 0 && blocks[i].Kind == "summary"; i-- {
			// Share the transcript's collapsed title, not its latest body line.
			// Roster timers already supply elapsed time separately.
			if label := blocks[i].Label; label != "" {
				headers = append(headers, ansi.Strip(p.Inline(label)))
				continue
			}
			section := blocks[i]
			section.Collapsed, section.Elapsed, section.Hovered = true, "", false
			if rows := p.blockRows(section, max(1, width)+2); len(rows) > 0 {
				headers = append(headers, strings.TrimPrefix(ansi.Strip(rows[0]), "• "))
			}
		}
		slices.Reverse(headers)
		return Dim + strings.Join(headers, ", ") + Undim
	case "final":
		text := firstLine(block.Body)
		if block.Journal != nil {
			text = "Final answer"
			for _, group := range block.Journal.Groups {
				if len(group.Answers) > 0 {
					text = firstLine(group.Answers[0].Text)
					break
				}
			}
		}
		// The roster's status glyph already marks a sent final answer.
		return text
	case "reads":
		var names []string
		for _, read := range block.Reads {
			name := read.Path
			if block.Verb != "Search" {
				name = name[strings.LastIndex(name, "/")+1:]
			}
			names = append(names, name)
		}
		return SummaryVerb(block.Verb) + strings.Join(names, ", ") + more
	case "op":
		if block.VCS() {
			return p.vcsSummary(block) + more
		}
		detail := strings.TrimSpace(p.Label(block.Verb, block.Label))
		if detail == "" || strings.HasPrefix(ansi.Strip(detail), "·") {
			code, _, _ := strings.Cut(block.Code, "\n")
			if block.Fenced {
				code = strings.Join(p.Highlight(block.Lang, code), " ")
			} else {
				code = p.code(block.Verb, code)
			}
			detail = strings.TrimSpace(code + " " + detail)
		}
		if block.ExitCode != 0 && (block.Verb == "Run" || block.Verb == "Skill" || block.Verb == "Capture" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, block.Verb)) {
			detail += " " + Red + fmt.Sprintf("(exit %d)", block.ExitCode) + Reset
		}
		verb := block.Verb
		if block.Running {
			verb = "Running"
		}
		return SummaryVerb(verb) + detail + more
	case "message":
		text := firstLine(block.Body)
		if text == "" {
			text = strings.TrimSuffix(block.Verb, ":")
		}
		return p.messageDirection(block) + " " + text
	case "start":
		return Green + "▶ Started" + Reset + " " + p.Inline(block.Label)
	case "compaction":
		return Amber + "◉ Context compacted" + Reset
	case "error":
		return Red + "✗ " + ErrorPreview(block.Body) + Reset
	}
	return firstLine(block.Body)
}

// Route names both ends of a message, sender first.
func (p Painter) Route(block Block) string {
	return p.recipient(block.From) + Dim + " → " + Undim + p.recipient(block.To)
}

// liveActivityCard frames rows under a titled top edge. A glowing card was
// just opened from another pane.
func liveActivityCard(title string, rows []string, width int, glow bool) []string {
	return Card(title, "", rows, width, glow)
}

// Card frames a finished answer: a rounded green edge with its title on the
// top border and optional dim right text, such as a time.
func Card(title, right string, rows []string, width int, glow bool) []string {
	edge := "\x1b[38;2;80;120;90m"
	if glow {
		edge = "\x1b[1m" + Green
	}
	inner := width - 4
	title = ansi.Truncate(title, max(0, width-6), "…")
	if right != "" && width-7-ansi.StringWidth(title)-ansi.StringWidth(right) >= 2 {
		right = " " + Reset + Dim + right + Undim + edge + " "
	} else {
		right = ""
	}
	top := edge + "╭─ " + Reset + title + edge + " " + strings.Repeat("─", max(0, width-5-ansi.StringWidth(title)-ansi.StringWidth(right))) + right + "╮" + Reset
	lines := []string{ansi.Truncate(top, width, "")}
	for _, row := range rows {
		row = ansi.Truncate(row, inner, "…")
		lines = append(lines, edge+"│"+Reset+" "+row+strings.Repeat(" ", max(0, inner-ansi.StringWidth(row)))+" "+edge+"│"+Reset)
	}
	return append(lines, edge+"╰"+strings.Repeat("─", max(0, width-2))+"╯"+Reset)
}

// Tree joins operation rows into one tree: ├ before each but the
// last, └ before the last, and a rail beside continuation rows.
func Tree(parts [][]string) []string {
	var lines []string
	for i, part := range parts {
		lead, rail := "├ ", "│ "
		if i == len(parts)-1 {
			lead, rail = "└ ", "  "
		}
		for k, line := range part {
			if k == 0 {
				lines = append(lines, Dim+lead+Undim+line)
			} else {
				lines = append(lines, Dim+rail+Undim+line)
			}
		}
	}
	return lines
}

// messageDirection is relative to the row's owner, not the transport recipient.
func (p Painter) messageDirection(block Block) string {
	direction, peer := "→ ", block.To
	if block.Owner == block.To {
		direction, peer = "← ", block.From
	}
	return Dim + direction + Undim + p.recipient(peer)
}

func ResultCount(count *int) string {
	if count == nil {
		return ""
	}
	return Dim + fmt.Sprintf(" (%d results)", *count) + Undim
}

// RunElapsed shares the compact duration grammar used by native status timers.
func RunElapsed(block Block, now time.Time) string {
	elapsed := block.Duration
	if block.Running && !block.Started.IsZero() {
		elapsed = now.Sub(block.Started)
	}
	if elapsed <= 3*time.Millisecond {
		return ""
	}
	var text string
	if elapsed < time.Second {
		text = elapsed.Truncate(time.Millisecond).String()
	} else {
		elapsed = elapsed.Truncate(time.Second)
		text = elapsed.String()
		if elapsed >= time.Minute && elapsed%time.Minute == 0 {
			text = strings.TrimSuffix(text, "0s")
			if elapsed%time.Hour == 0 {
				text = strings.TrimSuffix(text, "0m")
			}
		}
	}
	return text
}
