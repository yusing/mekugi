package router

import (
	"context"
	"fmt"
	"strings"

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
	case "Read", "View", "Open", "List", "Check":
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
	return color + name + liveActivityReset
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
	case "Run", "Search", "Send":
		return strings.Join(p.highlight("bash", code), " ")
	case "Read", "Edit", "Create", "Delete", "Update", "Write", "View", "Move", "Rename":
		return liveActivityPath(code)
	case "MCP", "Tool":
		return "\x1b[38;5;80m" + code + "\x1b[39m"
	}
	return p.theme.Accent() + code + "\x1b[39m"
}

func (p *liveActivityPainter) label(verb, label string) string {
	var out strings.Builder
	for i := 0; i < len(label); {
		if code, end, ok := liveActivityCodeSpan(label, i); ok {
			out.WriteString(p.code(verb, code))
			i = end
			continue
		}
		next := strings.IndexByte(label[i+1:], '`')
		text := label[i:]
		if next >= 0 {
			text = label[i : i+1+next]
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
		return append([]string{liveActivityGreen + "✓ Final answer" + liveActivityReset}, liveActivityIndent(p.markdown(block.body, width-2), "  ")...)
	case "reads":
		var items []string
		for _, read := range block.reads {
			item := liveActivityPath(read.path)
			if len(read.ranges) > 0 {
				item += " " + liveActivityDim + strings.Join(read.ranges, ", ") + liveActivityUndim
			}
			items = append(items, item)
		}
		return liveActivityHang(liveActivityVerb("Read"), strings.Join(items, liveActivityDim+" · "+liveActivityUndim), width)
	case "op":
		label := p.label(block.verb, block.label)
		code, body := block.code, block.body
		// A single-line program or argument reads best on the operation row.
		if code != "" && !strings.Contains(code, "\n") && (label == "" || !block.fenced) {
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
		return lines
	case "message":
		// The envelope glyph already says a message arrived; other headlines stay.
		head := liveActivityDim + "✉" + liveActivityUndim + " " + p.agent(block.from) + liveActivityDim + " → " + liveActivityUndim + p.agent(block.to)
		if headline := strings.TrimSuffix(block.verb, ":"); headline != "Message received" && headline != "Message received." {
			head += "  " + liveActivityDim + headline + liveActivityUndim
		}
		lines := []string{ansi.Truncate(head, width, "…")}
		bar := liveAgentGutter(block.from, p.theme) + "┃" + liveActivityReset + " "
		return append(lines, liveActivityIndent(p.markdown(block.body, width-2), bar)...)
	case "start":
		lines := liveActivityHang(liveActivityGreen+"\x1b[1m▶ Started"+liveActivityReset+"  ", liveActivityDim+block.label+liveActivityUndim, width)
		if block.body != "" {
			lines = append(lines, liveActivityDim+"  prompt"+liveActivityUndim)
			lines = append(lines, liveActivityIndent(p.markdown(block.body, width-2), liveActivityDim+"┃"+liveActivityUndim+" ")...)
		}
		return lines
	case "error":
		return liveActivityIndent(liveActivityWrap(liveActivityRed+block.body+liveActivityReset, width-2, false), liveActivityRed+"✗"+liveActivityReset+" ")
	}
	return p.markdown(block.body, width)
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
				return plain
			}
		}
		return ""
	}
	switch block.kind {
	case "final":
		return liveActivityGreen + "✓ " + liveActivityReset + firstLine(block.body)
	case "reads":
		var names []string
		for _, read := range block.reads {
			names = append(names, read.path[strings.LastIndex(read.path, "/")+1:])
		}
		return liveActivitySummaryVerb("Read") + strings.Join(names, ", ") + more
	case "op":
		detail := strings.TrimSpace(ansi.Strip(p.label(block.verb, block.label)))
		if detail == "" || strings.HasPrefix(detail, "·") {
			detail = strings.TrimSpace(firstLine(block.code) + " " + detail)
		}
		return liveActivitySummaryVerb(block.verb) + detail + more
	case "message":
		text := firstLine(block.body)
		if text == "" {
			text = strings.TrimSuffix(block.verb, ":")
		}
		return liveActivityDim + "✉ → " + liveActivityUndim + p.agent(block.to) + " " + text
	case "start":
		return liveActivityGreen + "▶ Started" + liveActivityReset + " " + liveActivityDim + block.label + liveActivityUndim
	case "error":
		return liveActivityRed + "✗ " + firstLine(block.body) + liveActivityReset
	}
	return firstLine(block.body)
}
