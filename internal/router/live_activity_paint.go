package router

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

const (
	liveActivityDim   = "\x1b[2m"
	liveActivityUndim = "\x1b[22m"
	liveActivityReset = "\x1b[0m"
	liveActivityGreen = "\x1b[38;5;114m"
	liveActivityRed   = "\x1b[38;5;203m"
	liveActivityAmber = "\x1b[38;5;214m"
)

// liveActivityPainter lays out parsed blocks as terminal rows. Syntax colors
// come from the same renderer and theme as the live diff pane.
type liveActivityPainter struct {
	theme  livediff.Theme
	syntax livediff.Renderer
}

func liveActivityVerbColor(verb string) string {
	first, _, _ := strings.Cut(verb, " ")
	switch first {
	case "Read", "Inspect", "View", "Open", "List", "Check":
		return "\x1b[38;5;75m"
	case "Search", "Find":
		return "\x1b[38;5;141m"
	case "Run", "Send", "Sleep", "Wait", "Still", "Stop":
		return liveActivityAmber
	case "Edit", "Create", "Delete", "Update", "Write", "Move", "Rename":
		return liveActivityGreen
	case "MCP", "Tool", "Browse", "Generate":
		return "\x1b[38;5;80m"
	}
	return ""
}

// liveAgentGutter is the unbolded agent color used for feed gutters.
func liveAgentGutter(name string, theme livediff.Theme) string {
	if color := liveAgentColor(name); color != "" {
		return strings.Replace(color, "1;", "", 1)
	}
	return theme.Accent()
}

func (p *liveActivityPainter) agent(name string) string {
	color := liveAgentColor(name)
	if color == "" {
		color = "\x1b[1m" + p.theme.Accent()
	}
	return color + agentDisplayName(name) + liveActivityReset
}

func (p *liveActivityPainter) recipient(name string) string {
	return liveAgentGutter(name, p.theme) + agentDisplayName(name) + liveActivityReset
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
func (p *liveActivityPainter) highlight(lang, source string) []string {
	if strings.EqualFold(lang, "diff") {
		var lines []string
		for line := range strings.SplitSeq(source, "\n") {
			switch {
			case strings.HasPrefix(line, "+"):
				line = liveActivityGreen + line + liveActivityReset
			case strings.HasPrefix(line, "-"):
				line = liveActivityRed + line + liveActivityReset
			case strings.HasPrefix(line, "@@"):
				line = liveActivityDim + line + liveActivityReset
			}
			lines = append(lines, line)
		}
		return lines
	}
	lines, err := p.syntax.ColorSource(context.Background(), p.theme, liveActivityLanguagePath(lang), source)
	if err != nil || len(lines) == 0 {
		return strings.Split(source, "\n")
	}
	return lines
}

// liveActivityWrap wraps styled text and restores the last color on
// continuation rows, as the live diff renderer does.
func liveActivityWrap(text string, width int, hard bool) []string {
	width = max(1, width)
	var wrapped string
	if hard {
		wrapped = ansi.Hardwrap(text, width, true)
	} else {
		wrapped = ansi.Wrap(text, width, "")
	}
	var lines []string
	carry := ""
	for line := range strings.SplitSeq(wrapped, "\n") {
		line = carry + line
		if start := strings.LastIndex(line, "\x1b["); start >= 0 {
			if end := strings.IndexByte(line[start:], 'm'); end >= 0 {
				carry = line[start : start+end+1]
			}
		}
		lines = append(lines, line)
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
		for _, line := range liveActivityWrap(label, width-2, false) {
			lines = append(lines, "  "+line)
		}
		return lines
	}
	var lines []string
	for i, line := range liveActivityWrap(label, width-indent, false) {
		if i == 0 {
			lines = append(lines, lead+line)
		} else {
			lines = append(lines, strings.Repeat(" ", indent)+line)
		}
	}
	return lines
}

func liveActivityPath(path string) string {
	if i := strings.LastIndex(strings.TrimSuffix(path, "/"), "/"); i >= 0 {
		return liveActivityDim + path[:i+1] + liveActivityUndim + "\x1b[1m" + path[i+1:] + liveActivityUndim
	}
	return "\x1b[1m" + path + liveActivityUndim
}

// code styles a code span according to the operation that carries it.
func (p *liveActivityPainter) code(verb, code string) string {
	first, _, _ := strings.Cut(verb, " ")
	switch first {
	case "Run", "Send":
		return strings.Join(p.highlight("bash", code), " ")
	case "Search":
		// Search operands are patterns, not shell programs. A shell lexer
		// miscolors regular-expression punctuation and can reset the verb color.
		return p.theme.Accent() + code + "\x1b[39m"
	case "Read", "Edit", "Create", "Delete", "Update", "Write", "View", "Move", "Rename":
		return liveActivityPath(code)
	case "MCP", "Tool":
		return "\x1b[38;5;80m" + code + "\x1b[39m"
	}
	return p.theme.Accent() + code + "\x1b[39m"
}

func (p *liveActivityPainter) label(verb, label string) string {
	var out strings.Builder
	searchTargets := false
	for i := 0; i < len(label); {
		if code, end, ok := liveActivityCodeSpan(label, i); ok {
			if verb == "Search" && searchTargets {
				out.WriteString(liveActivityVerbColor(verb) + liveActivityPath(code) + "\x1b[39m")
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
				word = liveActivityGreen + word + "\x1b[39m"
			case len(word) > 1 && word[0] == '-':
				word = liveActivityRed + word + "\x1b[39m"
			case word != "":
				word = liveActivityDim + word + liveActivityUndim
			}
			out.WriteString(word)
		}
		i += len(text)
	}
	return out.String()
}

// inline renders the commentary Markdown inline subset.
func (p *liveActivityPainter) inline(line string) string {
	var out strings.Builder
	bold := false
	for i := 0; i < len(line); {
		if line[i] == '[' {
			if label, target, end, ok := liveActivityLink(line[i:]); ok {
				link := url.URL{Scheme: "file", Path: target}
				out.WriteString("\x1b]8;;" + link.String() + "\x1b\\" + p.theme.Accent() + "\x1b[4m" + label + "\x1b[24;39m\x1b]8;;\x1b\\")
				i += end
				continue
			}
		}
		if code, end, ok := liveActivityCodeSpan(line, i); ok {
			out.WriteString(p.theme.Accent() + code + "\x1b[39m")
			i = end
			continue
		}
		if strings.HasPrefix(line[i:], "**") {
			bold = !bold
			if bold {
				out.WriteString("\x1b[1m")
			} else {
				out.WriteString(liveActivityUndim)
			}
			i += 2
			continue
		}
		out.WriteByte(line[i])
		i++
	}
	if bold {
		out.WriteString(liveActivityUndim)
	}
	return out.String()
}

// Only local absolute paths become terminal links. Relative or malformed
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
	if end == 0 || !strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\x00\x1b\r\n") {
		return "", "", 0, false
	}
	return s[1:close], target, end, true
}

// markdown renders authored text: fenced programs are highlighted under a
// gutter, list items hang, and tables keep their rows instead of wrapping.
func (p *liveActivityPainter) markdown(text string, width int) []string {
	var lines, program []string
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
		if delimiter, ok := toolActivityFenceDelimiter(line); ok {
			fence, lang = delimiter, strings.TrimSpace(line[len(delimiter):])
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		indent := line[:len(line)-len(trimmed)]
		switch {
		case strings.HasPrefix(trimmed, "|"):
			lines = append(lines, ansi.Truncate(liveActivityDim+line+liveActivityUndim, width, "…"))
		case strings.HasPrefix(trimmed, "#"):
			lines = append(lines, liveActivityWrap("\x1b[1m"+p.inline(strings.TrimLeft(trimmed, "# "))+liveActivityUndim, width, false)...)
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			lines = append(lines, liveActivityHang(indent+liveActivityDim+"•"+liveActivityUndim+" ", p.inline(trimmed[2:]), width)...)
		default:
			lines = append(lines, liveActivityWrap(p.inline(line), width, false)...)
		}
	}
	if fence != "" {
		lines = append(lines, p.program(lang, strings.Join(program, "\n"), width)...)
	}
	for len(lines) > 0 && ansi.Strip(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func (p *liveActivityPainter) program(lang, source string, width int) []string {
	var lines []string
	for _, line := range p.highlight(lang, source) {
		for _, part := range liveActivityWrap(line, width-2, true) {
			lines = append(lines, liveActivityDim+"│"+liveActivityUndim+" "+part)
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
func (p *liveActivityPainter) block(block liveActivityBlock, width int) []string {
	width = max(8, width)
	switch block.kind {
	case "final":
		if block.journal != nil {
			return p.journal(block.journal, width, block.compact)
		}
		return append([]string{liveActivityGreen + "✓ Final answer" + liveActivityReset}, liveActivityIndent(p.markdown(block.body, width-2), "  ")...)
	case "reads":
		var items []string
		for _, read := range block.reads {
			item := liveActivityPath(read.path)
			if block.verb == "Search" {
				item = p.code(block.verb, read.path)
			}
			if len(read.ranges) > 0 {
				item += " " + liveActivityDim + strings.Join(read.ranges, ", ") + liveActivityUndim
			}
			items = append(items, item)
		}
		return liveActivityHang(liveActivityVerb(block.verb), strings.Join(items, liveActivityDim+" · "+liveActivityUndim), width)
	case "op":
		label := p.label(block.verb, block.label)
		code, body := block.code, block.body
		exit := ""
		if block.exitCode != 0 && (block.verb == "Run" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, block.verb)) {
			exit = liveActivityRed + fmt.Sprintf("(exit %d)", block.exitCode) + liveActivityReset
		}
		if block.verb == "Run" && block.fenced && label == "" && strings.Contains(code, "\n") && width-ansi.StringWidth(liveActivityVerb(block.verb)) >= 4 {
			lead := liveActivityVerb(block.verb)
			indent := ansi.StringWidth(lead)
			lines := p.program(block.lang, code, width-indent)
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
				lines = append(lines, liveActivityIndent(p.markdown(body, width-2), "  ")...)
			}
			return lines
		}
		// Keep Code Mode source below its long heading, regardless of source line count.
		// Other single-line programs or arguments read best on the operation row.
		if code != "" && !strings.Contains(code, "\n") && (label == "" || !block.fenced) && !(block.fenced && block.verb == "Run JavaScript") {
			inline := p.code(block.verb, code)
			if block.fenced {
				inline = strings.Join(p.highlight(block.lang, code), " ")
			}
			label, code = strings.TrimSpace(label+" "+inline), ""
		}
		lines := liveActivityHang(liveActivityVerb(block.verb), label, width)
		if code != "" {
			lang := block.lang
			if !block.fenced && (block.verb == "MCP" || strings.HasPrefix(block.verb, "Tool")) {
				lang = "json"
			}
			lines = append(lines, liveActivityIndent(p.program(lang, code, width-2), "  ")...)
		}
		if body != "" {
			lines = append(lines, liveActivityIndent(p.markdown(body, width-2), "  ")...)
		}
		if exit != "" {
			last := len(lines) - 1
			if ansi.StringWidth(lines[last])+1+ansi.StringWidth(exit) <= width {
				lines[last] += " " + exit
			} else {
				lines = append(lines, strings.Repeat(" ", ansi.StringWidth(liveActivityVerb(block.verb)))+exit)
			}
		}
		return lines
	case "message":
		// The envelope glyph already says a message arrived; other headlines stay.
		head := p.messageDirection(block)
		if headline := strings.TrimSuffix(block.verb, ":"); headline != "Message received" && headline != "Message received." {
			head += "  " + liveActivityDim + headline + liveActivityUndim
		}
		lines := []string{ansi.Truncate(head, width, "…")}
		bar := liveAgentGutter(block.from, p.theme) + "┃" + liveActivityReset + " "
		return append(lines, liveActivityIndent(p.markdown(block.body, width-2), bar)...)
	case "start":
		lines := liveActivityHang(liveActivityGreen+"\x1b[1m▶ Started"+liveActivityReset+"  ", p.inline(block.label), width)
		if block.body != "" {
			lines = append(lines, liveActivityIndent(p.markdown(block.body, width-2), "  ")...)
		}
		return lines
	case "compaction":
		return []string{liveActivityAmber + "◉ Context compacted" + liveActivityReset}
	case "filter":
		indent := min(ansi.StringWidth(liveActivityVerb("Run")), width/2)
		rows := liveActivityWrap(block.body, width-indent, true)
		for i := range rows {
			rows[i] = strings.Repeat(" ", indent) + p.theme.Foreground(chroma.Comment) + liveActivityDim + rows[i] + liveActivityReset
		}
		return rows
	case "error":
		return liveActivityIndent(liveActivityWrap(liveActivityRed+block.body+liveActivityReset, width-2, false), liveActivityRed+"✗"+liveActivityReset+" ")
	}
	return p.markdown(block.body, width)
}

// journal lays out a final journal result: a heading with its answer and
// change totals, each question with its answers, then recorded changes. The
// agent heading already names the author. The shared feed keeps questions to
// one row so clipping reaches the answers.
func (p *liveActivityPainter) journal(journal *liveActivityJournal, width int, compact bool) []string {
	answers := 0
	for _, group := range journal.groups {
		answers += len(group.answers)
	}
	head := liveActivityGreen + "✓ Final answer" + liveActivityReset
	var facts []string
	switch {
	case answers == 1:
		facts = append(facts, "1 answer")
	case answers > 1:
		facts = append(facts, fmt.Sprintf("%d answers", answers))
	}
	if len(journal.stats) > 0 {
		added, removed := 0, 0
		for _, stat := range journal.stats {
			a, errA := strconv.Atoi(stat.added)
			r, errR := strconv.Atoi(stat.removed)
			if errA == nil && errR == nil {
				added, removed = added+a, removed+r
			}
		}
		files := "1 file"
		if len(journal.stats) > 1 {
			files = fmt.Sprintf("%d files", len(journal.stats))
		}
		facts = append(facts, files+" "+liveActivityGreen+fmt.Sprintf("+%d", added)+"\x1b[39m "+liveActivityRed+fmt.Sprintf("-%d", removed)+"\x1b[39m")
	}
	if len(facts) > 0 {
		head += liveActivityDim + " · " + strings.Join(facts, " · ") + liveActivityUndim
	}
	lines := []string{ansi.Truncate(head, width, "…")}
	if journal.empty {
		lines = append(lines, "  "+liveActivityDim+"No journal entries"+liveActivityUndim)
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
	accent := "\x1b[1m" + p.theme.Accent()
	for _, group := range journal.groups {
		if group.question != "" {
			question := p.markdown(group.question, width-4)
			if compact && len(question) > 1 {
				flat := strings.Join(strings.Fields(ansi.Strip(strings.Join(question, " "))), " ")
				question = []string{ansi.Truncate(flat, width-4, "…")}
			}
			hang(accent+"Q"+liveActivityReset+" ", liveActivityIndent(question, "\x1b[1m"))
		}
		for _, answer := range group.answers {
			lead := liveActivityGreen + "\x1b[1mA" + liveActivityReset + " "
			if group.question == "" {
				lead = liveActivityDim + "•" + liveActivityUndim + " "
			}
			body := p.markdown(answer.text, width-4)
			if answers > 1 {
				hang(lead, []string{liveActivityDim + answer.id + liveActivityUndim})
				lines = append(lines, liveActivityIndent(body, "    ")...)
				continue
			}
			hang(lead, body)
		}
	}
	if journal.clipped {
		lines = append(lines, "  "+liveActivityDim+"… full answer in Codex completion"+liveActivityUndim)
	}
	if len(journal.stats) > 0 || journal.changes != "" && len(journal.notes) > 0 {
		lines = append(lines, "  "+liveActivityVerb("Changes")+liveActivityDim+journal.changes+liveActivityUndim)
		addedWidth, removedWidth := 0, 0
		for _, stat := range journal.stats {
			addedWidth, removedWidth = max(addedWidth, len(stat.added)), max(removedWidth, len(stat.removed))
		}
		for _, stat := range journal.stats {
			counts := liveActivityGreen + fmt.Sprintf("%*s", addedWidth+1, "+"+stat.added) + "\x1b[39m " + liveActivityRed + fmt.Sprintf("%-*s", removedWidth+1, "-"+stat.removed) + "\x1b[39m "
			lines = append(lines, "    "+ansi.Truncate(counts+liveActivityPath(stat.path), width-4, "…"))
		}
	}
	// "No recorded changes." is the ordinary read-only outcome, not news.
	for _, note := range journal.notes {
		if note != "No recorded changes." && note != "No recorded file changes." {
			lines = append(lines, liveActivityIndent(liveActivityWrap(liveActivityDim+note+liveActivityUndim, width-4, false), "    ")...)
		}
	}
	return lines
}

// liveActivityVerb pads verbs to a common column so arguments line up.
func liveActivityVerb(verb string) string {
	if verb == "" {
		return ""
	}
	return liveActivityVerbColor(verb) + "\x1b[1m" + verb + liveActivityReset + strings.Repeat(" ", max(1, 7-ansi.StringWidth(verb)))
}

func liveActivitySummaryVerb(verb string) string {
	return liveActivityVerbColor(verb) + verb + liveActivityReset + " "
}

// summary is the roster's one-line view of an agent's current activity.
func (p *liveActivityPainter) summary(blocks []liveActivityBlock) string {
	for len(blocks) > 0 && blocks[len(blocks)-1].kind == "filter" {
		blocks = blocks[:len(blocks)-1]
	}
	if len(blocks) == 0 {
		return ""
	}
	block := blocks[len(blocks)-1]
	more := ""
	if len(blocks) > 1 {
		more = liveActivityDim + fmt.Sprintf(" · +%d more", len(blocks)-1) + liveActivityUndim
	}
	firstLine := func(text string) string {
		for _, line := range p.markdown(text, 1<<16) {
			if plain := strings.TrimSpace(strings.TrimPrefix(ansi.Strip(line), "│")); plain != "" {
				return strings.TrimSpace(strings.TrimPrefix(plain, "• "))
			}
		}
		return ""
	}
	switch block.kind {
	case "final":
		text := firstLine(block.body)
		if block.journal != nil {
			text = "Final answer"
			for _, group := range block.journal.groups {
				if len(group.answers) > 0 {
					text = firstLine(group.answers[0].text)
					break
				}
			}
		}
		// The roster's status glyph already marks a sent final answer.
		return text
	case "reads":
		var names []string
		for _, read := range block.reads {
			name := read.path
			if block.verb != "Search" {
				name = name[strings.LastIndex(name, "/")+1:]
			}
			names = append(names, name)
		}
		return liveActivitySummaryVerb(block.verb) + strings.Join(names, ", ") + more
	case "op":
		detail := strings.TrimSpace(p.label(block.verb, block.label))
		if detail == "" || strings.HasPrefix(ansi.Strip(detail), "·") {
			code, _, _ := strings.Cut(block.code, "\n")
			if block.fenced {
				code = strings.Join(p.highlight(block.lang, code), " ")
			} else {
				code = p.code(block.verb, code)
			}
			detail = strings.TrimSpace(code + " " + detail)
		}
		if block.exitCode != 0 && (block.verb == "Run" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, block.verb)) {
			detail += " " + liveActivityRed + fmt.Sprintf("(exit %d)", block.exitCode) + liveActivityReset
		}
		return liveActivitySummaryVerb(block.verb) + detail + more
	case "message":
		text := firstLine(block.body)
		if text == "" {
			text = strings.TrimSuffix(block.verb, ":")
		}
		return p.messageDirection(block) + " " + text
	case "start":
		return liveActivityGreen + "▶ Started" + liveActivityReset + " " + p.inline(block.label)
	case "compaction":
		return liveActivityAmber + "◉ Context compacted" + liveActivityReset
	case "error":
		return liveActivityRed + "✗ " + firstLine(block.body) + liveActivityReset
	}
	return firstLine(block.body)
}

// messageDirection is relative to the row's owner, not the transport recipient.
func (p liveActivityPainter) messageDirection(block liveActivityBlock) string {
	direction, peer := "to ", block.to
	if block.owner == block.to {
		direction, peer = "from ", block.from
	}
	// Many fonts draw ✉ wider than its one cell; the extra space keeps it off the label.
	return liveActivityDim + "✉  " + direction + liveActivityUndim + p.recipient(peer)
}
