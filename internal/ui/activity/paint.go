package activity

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

const (
	Dim   = livediff.Subtle
	Undim = "\x1b[22m" + livediff.SubtleReset
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
}

func VerbColor(verb string) string {
	first, _, _ := strings.Cut(verb, " ")
	switch first {
	case "Read", "Inspect", "View", "Open", "List", "Check":
		return "\x1b[38;5;75m"
	case "Search", "Find", "Skill":
		return "\x1b[38;5;141m"
	case "Run", "Send", "Sleep", "Wait", "Still", "Stop":
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
	if strings.EqualFold(lang, "diff") {
		var lines []string
		for line := range strings.SplitSeq(source, "\n") {
			switch {
			case strings.HasPrefix(line, "+"):
				line = Green + line + Reset
			case strings.HasPrefix(line, "-"):
				line = Red + line + Reset
			case strings.HasPrefix(line, "@@"):
				line = Dim + line + Reset
			}
			lines = append(lines, line)
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
	parser := ansi.GetParser()
	defer func() {
		parser.SetHandler(ansi.Handler{})
		ansi.PutParser(parser)
	}()
	parser.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd == 'm' {
			uv.ReadStyle(params, &style)
		}
	}})
	carry := ""
	for line := range strings.SplitSeq(wrapped, "\n") {
		lines = append(lines, carry+line)
		parser.Parse([]byte(line))
		carry = style.String()
		if style.IsZero() {
			// Enclosing bands recognize this canonical reset to reapply their background.
			carry = Reset
		}
	}
	return lines
}

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
				out.WriteString(VerbColor(verb) + Path(code) + "\x1b[39m")
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
			out.WriteString(p.Theme.Accent() + code + "\x1b[39m")
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

// Absolute local paths and HTTP(S) URLs become terminal links. Relative or malformed
// Markdown remains visible verbatim rather than guessing a filesystem target.
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
	if end == 0 || !(strings.HasPrefix(target, "/") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://")) || strings.ContainsAny(target, "\x00\x1b\r\n") {
		return "", "", 0, false
	}
	return s[1:close], target, end, true
}

// markdown renders authored text: fenced programs are highlighted under a
// gutter, list items hang, and tables keep their rows instead of wrapping.
func (p *Painter) Markdown(text string, width int) []string {
	var lines, program, quote []string
	flushQuote := func() {
		if len(quote) > 0 {
			lines = append(lines, p.Quote(strings.Join(quote, "\n"), width)...)
			quote = nil
		}
	}
	fence, lang := "", ""
	for line := range strings.SplitSeq(text, "\n") {
		if fence != "" {
			if line != fence {
				program = append(program, line)
				continue
			}
			lines = append(lines, p.program(lang, strings.Join(program, "\n"), width)...)
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
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		switch {
		case strings.HasPrefix(trimmed, "|"):
			lines = append(lines, ansi.Truncate(Dim+line+Undim, width, "…"))
		case strings.HasPrefix(trimmed, "#"):
			lines = append(lines, Wrap("\x1b[1m"+p.Inline(strings.TrimLeft(trimmed, "# "))+Undim, width, false)...)
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			lines = append(lines, liveActivityHang(indent+Dim+"•"+Undim+" ", p.Inline(trimmed[2:]), width)...)
		default:
			lines = append(lines, Wrap(p.Inline(line), width, false)...)
		}
	}
	flushQuote()
	if fence != "" {
		lines = append(lines, p.program(lang, strings.Join(program, "\n"), width)...)
	}
	for len(lines) > 0 && ansi.Strip(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// quote keeps a visible rail on every wrapped row and renders the quoted
// Markdown normally, including lists, nested quotes, and fenced code.
func (p *Painter) Quote(text string, width int) []string {
	rows := p.Markdown(text, max(1, width-2))
	if len(rows) == 0 {
		rows = []string{""}
	}
	for i, row := range rows {
		rows[i] = ansi.Truncate(Dim+"│"+Undim+" "+row, max(1, width), "")
	}
	return rows
}

func (p *Painter) program(lang, source string, width int) []string {
	var lines []string
	for _, line := range p.Highlight(lang, source) {
		for _, part := range Wrap(line, width-2, true) {
			lines = append(lines, Dim+"│"+Undim+" "+part)
		}
	}
	return lines
}

func liveActivityIndent(lines []string, prefix string) []string {
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return lines
}

// block renders one parsed block within width columns.
func (p *Painter) Block(block Block, width int) []string {
	width = max(8, width)
	switch block.Kind {
	case "summary":
		// Codex keeps summary bodies in detailed transcript, dim and italic,
		// with a bullet rather than a separate "Reasoning summary" card.
		body := ReasoningSummaryBody(block.Body)
		if body == "" {
			return nil
		}
		style := Dim + "\x1b[3m"
		rows := p.Markdown(body, width-2)
		for i, row := range rows {
			row = strings.NewReplacer(Reset, Reset+style, "\x1b[22m", "\x1b[22m"+style, "\x1b[24;39m", "\x1b[24m"+Dim, "\x1b[39m", Dim, "\x1b[23m", style).Replace(row)
			prefix := "  "
			if i == 0 {
				prefix = "• "
			}
			rows[i] = style + prefix + row + Reset
		}
		return rows
	case "final":
		if block.Journal != nil {
			return p.Journal(block.Journal, width, true)
		}
		return append([]string{Green + "✓ Final answer" + Reset}, liveActivityIndent(p.Markdown(block.Body, width-2), "  ")...)
	case "reads":
		var items []string
		for _, read := range block.Reads {
			item := Path(read.Path)
			if block.Verb == "Search" || block.Verb == "Skill" {
				item = p.code(block.Verb, read.Path)
			}
			if len(read.Ranges) > 0 {
				item += " " + Dim + strings.Join(read.Ranges, ", ") + Undim
			}
			items = append(items, item)
		}
		return liveActivityHang(Verb(block.Verb), strings.Join(items, Dim+" · "+Undim)+ResultCount(block.Results), width)
	case "op":
		label := p.Label(block.Verb, block.Label) + ResultCount(block.Results)
		code, body := block.Code, block.Body
		exit := ""
		if block.ExitCode != 0 && (block.Verb == "Run" || block.Verb == "Skill" || block.Verb == "Capture" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, block.Verb)) {
			exit = Red + fmt.Sprintf("(exit %d)", block.ExitCode) + Reset
		}
		if block.Verb == "Run" && block.Fenced && label == "" && strings.Contains(code, "\n") && width-ansi.StringWidth(Verb(block.Verb)) >= 4 {
			lead := Verb(block.Verb)
			indent := ansi.StringWidth(lead)
			lines := p.program(block.Lang, code, width-indent)
			for i := range lines {
				if i == 0 {
					lines[i] = lead + lines[i]
				} else {
					lines[i] = strings.Repeat(" ", indent) + lines[i]
				}
			}
			if exit != "" {
				lines = append(lines, strings.Repeat(" ", indent)+exit)
			}
			if body != "" {
				lines = append(lines, liveActivityIndent(p.Markdown(body, width-2), "  ")...)
			}
			return lines
		}
		// Keep Code Mode source below its long heading, regardless of source line count.
		// Other single-line programs or arguments read best on the operation row.
		if code != "" && !strings.Contains(code, "\n") && (label == "" || !block.Fenced) && !(block.Fenced && block.Verb == "Run JavaScript") {
			inline := p.code(block.Verb, code)
			if block.Fenced {
				inline = strings.Join(p.Highlight(block.Lang, code), " ")
			}
			label, code = strings.TrimSpace(label+" "+inline), ""
		}
		lines := liveActivityHang(Verb(block.Verb), label, width)
		if code != "" {
			lang := block.Lang
			if !block.Fenced && (block.Verb == "MCP" || strings.HasPrefix(block.Verb, "Tool")) {
				lang = "json"
			}
			lines = append(lines, liveActivityIndent(p.program(lang, code, width-2), "  ")...)
		}
		if body != "" {
			lines = append(lines, liveActivityIndent(p.Markdown(body, width-2), "  ")...)
		}
		if exit != "" {
			last := len(lines) - 1
			if ansi.StringWidth(lines[last])+1+ansi.StringWidth(exit) <= width {
				lines[last] += " " + exit
			} else {
				lines = append(lines, strings.Repeat(" ", ansi.StringWidth(Verb(block.Verb)))+exit)
			}
		}
		return lines
	case "message":
		head := p.messageDirection(block)
		if headline := strings.TrimSuffix(block.Verb, ":"); headline != "" && headline != "Message received" && headline != "Message received." && headline != "Message sent" && headline != "Message sent." {
			head += "  " + Dim + headline + Undim
		}
		lines := []string{ansi.Truncate(head, width, "…")}
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
	case "filter":
		indent := min(ansi.StringWidth(Verb("Run")), width/2)
		rows := Wrap(block.Body, width-indent, true)
		for i := range rows {
			rows[i] = strings.Repeat(" ", indent) + Dim + rows[i] + Reset
		}
		return rows
	case "error":
		return liveActivityIndent(Wrap(Red+block.Body+Reset, width-2, false), Red+"✗"+Reset+" ")
	}
	return p.Markdown(block.Body, width)
}

// event renders a block for the native Activity log: a short label row, then
// the body flush with the gutter so narrow panes keep their full width.
// Operations keep their ordinary one-row-per-operation layout.
func (p *Painter) Event(block Block, width int) []string {
	width = max(8, width)
	label := func(head string, body []string) []string {
		return append([]string{ansi.Truncate(head, width, "…")}, body...)
	}
	done := Green + "✓" + Reset + Dim + " answer" + Undim
	switch block.Kind {
	case "message":
		return label(p.route(block), p.Markdown(block.Body, width))
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
			tail := *block.Journal
			tail.Groups, tail.empty = nil, false
			rows = append(rows, p.Journal(&tail, inner, false)...)
		}
		if width < 12 {
			return label(title, rows)
		}
		return liveActivityCard(title, rows, width)
	case "text":
		return p.Markdown(block.Body, width)
	}
	return p.Block(block, width)
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
	if len(journal.Stats) > 0 {
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
		facts = append(facts, files+" "+Green+fmt.Sprintf("+%d", added)+"\x1b[39m "+Red+fmt.Sprintf("-%d", removed)+"\x1b[39m")
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
		lines = append(lines, "  "+Verb("Changes")+Dim+journal.Changes+Undim)
		addedWidth, removedWidth := 0, 0
		for _, stat := range journal.Stats {
			addedWidth, removedWidth = max(addedWidth, len(stat.Added)), max(removedWidth, len(stat.Removed))
		}
		for _, stat := range journal.Stats {
			counts := Green + fmt.Sprintf("%*s", addedWidth+1, "+"+stat.Added) + "\x1b[39m " + Red + fmt.Sprintf("%-*s", removedWidth+1, "-"+stat.Removed) + "\x1b[39m "
			lines = append(lines, "    "+ansi.Truncate(counts+Path(stat.Path), width-4, "…"))
		}
	}
	// "No recorded changes." is the ordinary read-only outcome, not news.
	for _, note := range journal.notes {
		if note != "No recorded changes." && note != "No recorded file changes." {
			lines = append(lines, liveActivityIndent(Wrap(Dim+note+Undim, width-4, false), "    ")...)
		}
	}
	return lines
}

// Verb pads verbs to a common column so arguments line up.
func Verb(verb string) string {
	if verb == "" {
		return ""
	}
	return VerbColor(verb) + "\x1b[1m" + verb + Reset + strings.Repeat(" ", max(1, 7-ansi.StringWidth(verb)))
}

func SummaryVerb(verb string) string {
	return VerbColor(verb) + verb + Reset + " "
}

// summary is the roster's one-line view of an agent's current activity.
func (p *Painter) Summary(blocks []Block) string {
	for len(blocks) > 0 && blocks[len(blocks)-1].Kind == "filter" {
		blocks = blocks[:len(blocks)-1]
	}
	if len(blocks) == 0 {
		return ""
	}
	block := blocks[len(blocks)-1]
	more := ""
	if len(blocks) > 1 {
		more = Dim + fmt.Sprintf(" · +%d more", len(blocks)-1) + Undim
	}
	firstLine := func(text string) string {
		for _, line := range p.Markdown(text, 1<<16) {
			if plain := strings.TrimSpace(strings.TrimPrefix(ansi.Strip(line), "│")); plain != "" {
				return strings.TrimSpace(strings.TrimPrefix(plain, "• "))
			}
		}
		return ""
	}
	switch block.Kind {
	case "summary":
		return Dim + ReasoningSummaryHeader(block.Body) + Undim
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
		return SummaryVerb(block.Verb) + detail + more
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
		return Red + "✗ " + firstLine(block.Body) + Reset
	}
	return firstLine(block.Body)
}

// route names both ends of a message, sender first.
func (p Painter) route(block Block) string {
	return p.recipient(block.From) + Dim + " → " + Undim + p.recipient(block.To)
}

// liveActivityCard frames rows under a titled top edge.
func liveActivityCard(title string, rows []string, width int) []string {
	const edge = "\x1b[38;2;80;120;90m"
	inner := width - 4
	top := edge + "╭─ " + Reset + title + edge + " " + strings.Repeat("─", max(0, width-5-ansi.StringWidth(title))) + "╮" + Reset
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
