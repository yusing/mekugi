package router

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

// liveActivityPrompt marks the user's text in the composer and transcript.
const liveActivityPrompt = "\x1b[38;2;196;167;231m"

// userBand tints the user's prompts so turns are easy to find while scrolling.
// An undetected theme keeps the terminal background rather than guessing.
func userBand(theme livediff.Theme) string {
	switch theme {
	case livediff.LightTheme:
		return "\x1b[48;2;240;238;246m"
	case livediff.DarkTheme:
		return "\x1b[48;2;34;32;42m"
	}
	return ""
}

// conversationTool reports Main's own operations, which render as one
// indented group so adjacent reads merge exactly as they do in Activity.
func conversationTool(entry activityPaneEntry) bool {
	return entry.Agent == "Main" && entry.journal == nil && slices.Contains([]string{"tool", "command", "output_filter"}, entry.Kind)
}

// conversationMilestone reports a live Main journal milestone. Adjacent ones
// share one journal block.
func conversationMilestone(entry activityPaneEntry) bool {
	return entry.Agent == "Main" && entry.Kind == "text" && entry.journal != nil
}

// renderConversation lays Main out as a transcript rather than an activity
// feed: prompts on a tinted band, Main's own messages and milestones without
// author headings, and agent traffic under one-line headings. It reuses
// Activity's block parsing, painter and viewport logic.
func (v *liveActivityView) renderConversation(width int) liveActivityFeed {
	var feed liveActivityFeed
	v.questionRows = make(map[uint64]int)
	used := make(map[liveActivityRunKey]liveActivityRun)
	for i := 0; i < len(v.entries); {
		if !v.visible(v.entries[i]) {
			i++
			continue
		}
		last, j := i, i+1
		for _, same := range []func(activityPaneEntry) bool{conversationTool, conversationMilestone} {
			if !same(v.entries[i]) {
				continue
			}
			for ; j < len(v.entries) && (!v.visible(v.entries[j]) || same(v.entries[j])); j++ {
				if v.visible(v.entries[j]) {
					last = j
				}
			}
			break
		}
		key := liveActivityRunKey{v.entries[i].Seq, v.entries[last].Seq, width, 0, v.painter.theme, -1, true}
		run, ok := v.runs[key]
		if !ok {
			run = v.conversationItem(i, last, width)
		}
		used[key] = run
		if len(run.lines) == 0 {
			i = j
			continue
		}
		if len(feed.lines) > 0 {
			feed.lines = append(feed.lines, "")
			feed.heads = append(feed.heads, len(feed.lines)-1)
			feed.snippets = append(feed.snippets, liveActivitySnippet{})
			feed.questions = append(feed.questions, 0)
		}
		head := len(feed.lines)
		if entry := v.entries[i]; entry.Agent == "You" || entry.Kind == "start" || entry.Kind == "assignment" {
			v.questionRows[entry.Seq] = head
		}
		for range run.lines {
			feed.heads = append(feed.heads, head)
			feed.snippets = append(feed.snippets, liveActivitySnippet{})
		}
		feed.lines = append(feed.lines, run.lines...)
		feed.questions = append(feed.questions, run.questions...)
		i = j
	}
	v.runs = used
	return feed
}

type conversationLines struct {
	lines     []string
	questions []uint64
}

func (c *conversationLines) add(target uint64, lines ...string) {
	for _, line := range lines {
		c.lines = append(c.lines, line)
		c.questions = append(c.questions, target)
	}
}

// hang prefixes the first row with lead and the rest with an equal-width indent.
func (c *conversationLines) hang(lead, indent string, lines []string) {
	for k, line := range lines {
		if k == 0 {
			c.add(0, lead+line)
		} else {
			c.add(0, indent+line)
		}
	}
}

func (v *liveActivityView) conversationItem(first, last, width int) liveActivityRun {
	entry := v.entries[first]
	blocks := v.blocks[first]
	var out conversationLines
	p := &v.painter
	switch {
	case entry.Agent == "You":
		v.userItem(&out, entry, width)
	case conversationTool(entry):
		var group []liveActivityBlock
		for k := first; k <= last; k++ {
			if v.visible(v.entries[k]) {
				group = append(group, v.blocks[k]...)
			}
		}
		var parts [][]string
		for _, block := range mergeLiveActivityReads(group) {
			if block.kind == "filter" && len(parts) > 0 {
				parts[len(parts)-1] = append(parts[len(parts)-1], p.block(block, width-4)...)
				continue
			}
			parts = append(parts, p.block(block, width-4))
		}
		out.hang("  ", "  ", liveActivityTree(parts))
	case entry.Agent == "Main" && entry.journal != nil && len(blocks) == 1 && blocks[0].journal != nil:
		v.flushItem(&out, entry, blocks[0].journal, first, width)
	case conversationMilestone(entry):
		var milestones []string
		for k := first; k <= last; k++ {
			if v.visible(v.entries[k]) {
				milestones = append(milestones, livediff.Safe(v.entries[k].Text, false))
			}
		}
		v.milestoneItem(&out, entry, milestones, width)
	case entry.Agent == "Main" && entry.Kind == "text":
		out.add(0, mainHeading(p, entry, width))
		gutter := mainGutter(p)
		out.hang(gutter, gutter, p.markdown(livediff.Safe(entry.Text, false), width-2))
	default:
		v.agentItem(&out, entry, blocks, first, width)
	}
	return liveActivityRun{lines: out.lines, snippets: make([]liveActivitySnippet, len(out.lines)), questions: out.questions}
}

// conversationHeading is one item heading: a glyph, a name, optional dim
// detail, and the time right-aligned when it fits.
func conversationHeading(glyph, name, detail string, entry activityPaneEntry, width int) string {
	head := glyph + " " + name
	if detail != "" {
		head += " " + liveActivityDim + detail + liveActivityUndim
	}
	stamp := liveActivityDim + entry.Observed.Local().Format("15:04:05") + liveActivityUndim
	head = ansi.Truncate(head, width, "…")
	if gap := width - ansi.StringWidth(head) - ansi.StringWidth(stamp); gap >= 2 {
		head += strings.Repeat(" ", gap) + stamp
	}
	return head
}

// mainHeading and mainGutter mark Main's own replies the way agent traffic
// is marked, so every transcript item starts with who spoke and when.
func mainHeading(p *liveActivityPainter, entry activityPaneEntry, width int) string {
	return conversationHeading(p.theme.Accent()+"●"+liveActivityReset, "\x1b[1m"+p.theme.Accent()+"main"+liveActivityReset, "", entry, width)
}

func mainGutter(p *liveActivityPainter) string {
	return p.theme.Accent() + "┃" + liveActivityReset + " "
}

// milestoneItem labels journal milestones as such, under the accent gutter,
// so progress notes cannot be mistaken for Main's replies.
func (v *liveActivityView) milestoneItem(out *conversationLines, entry activityPaneEntry, milestones []string, width int) {
	accent := v.painter.theme.Accent()
	out.add(0, conversationHeading(accent+"◆"+liveActivityReset, "\x1b[1m"+accent+"journal"+liveActivityReset, "", entry, width))
	gutter := accent + "│" + liveActivityReset + " "
	for _, text := range milestones {
		rows := v.painter.markdown(text, width-4)
		if len(milestones) == 1 {
			rows = v.painter.markdown(text, width-2)
			out.hang(gutter, gutter, rows)
			continue
		}
		out.hang(gutter+"• ", gutter+"  ", rows)
	}
}

// flushItem shows a terminal journal flush: its milestones as a journal
// block, then each answer as Main's reply below a link to its question.
func (v *liveActivityView) flushItem(out *conversationLines, entry activityPaneEntry, journal *liveActivityJournal, index, width int) {
	var milestones []string
	answers := *journal
	answers.groups = nil
	for _, group := range journal.groups {
		if group.question != "" {
			answers.groups = append(answers.groups, group)
			continue
		}
		for _, item := range group.answers {
			milestones = append(milestones, item.text)
		}
	}
	if len(milestones) > 0 {
		v.milestoneItem(out, entry, milestones, width)
		if len(answers.groups) > 0 {
			out.add(0, "")
		}
	}
	if len(answers.groups) > 0 || len(milestones) == 0 {
		out.add(0, mainHeading(&v.painter, entry, width))
		gutter := mainGutter(&v.painter)
		v.journalItem(out, &answers, index, width, gutter, gutter)
	}
}

func (v *liveActivityView) userItem(out *conversationLines, entry activityPaneEntry, width int) {
	band := userBand(v.painter.theme)
	rows := v.painter.markdown(livediff.Safe(entry.Text, false), width-2)
	if len(rows) == 0 {
		rows = []string{""}
	}
	stamp := liveActivityDim + entry.Observed.Local().Format("15:04:05") + liveActivityUndim
	for k, row := range rows {
		lead := "  "
		if k == 0 {
			lead = liveActivityPrompt + "❯" + liveActivityReset + " "
		}
		line := ansi.Truncate(lead+row, width, "…")
		if k == 0 && ansi.StringWidth(line)+2+ansi.StringWidth(stamp) <= width {
			line += strings.Repeat(" ", width-ansi.StringWidth(line)-ansi.StringWidth(stamp)) + stamp
		}
		if band != "" {
			line = strings.ReplaceAll(line, liveActivityReset, liveActivityReset+band)
			line = band + line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line))) + "\x1b[49m"
		}
		out.add(0, line)
	}
}

// agentItem gives agent traffic a one-line heading naming the agent and what
// happened, with the body under that agent's colored gutter.
func (v *liveActivityView) agentItem(out *conversationLines, entry activityPaneEntry, blocks []liveActivityBlock, index, width int) {
	p := &v.painter
	agent, glyph, detail := entry.Agent, liveActivityDim+"●"+liveActivityUndim, ""
	var block liveActivityBlock
	if len(blocks) > 0 {
		block = blocks[0]
	}
	switch {
	case block.kind == "start":
		agent, glyph = block.to, liveActivityGreen+"▶"+liveActivityReset
		detail = "started"
		if block.label != "" {
			detail += " · " + ansi.Strip(p.inline(block.label))
		}
	case block.kind == "message" && block.from == "/root":
		agent, glyph = block.to, liveActivityDim+"→"+liveActivityUndim
	case block.kind == "message":
		agent, glyph = block.from, liveActivityDim+"←"+liveActivityUndim
	case block.kind == "final":
		glyph, detail = liveActivityGreen+"✓"+liveActivityReset, "finished"
	case block.kind == "error":
		glyph = liveActivityRed + "✗" + liveActivityReset
	}
	if agent == "" {
		agent = entry.Agent
	}
	out.add(0, conversationHeading(glyph, p.agent(agent), detail, entry, width))
	gutter := liveAgentGutter(agent, p.theme) + "│" + liveActivityReset + " "
	body := width - 2
	switch {
	case block.kind == "start" || block.kind == "message":
		out.hang(gutter, gutter, p.markdown(block.body, body))
	case block.kind == "final" && block.journal != nil:
		v.journalItem(out, block.journal, index, width, gutter, gutter)
	default:
		for _, block := range blocks {
			if block.kind == "error" {
				out.hang(gutter, gutter, liveActivityWrap(liveActivityRed+block.body+liveActivityReset, body, false))
				continue
			}
			if block.kind == "final" {
				// A plain answer links to the latest task it could answer.
				for _, question := range slices.Backward(v.entries[:index]) {
					if (question.Kind == "start" || question.Kind == "assignment") && question.assignment != nil && question.assignment.to == agent {
						out.add(question.Seq, gutter+v.linkLabel(question, body))
						break
					}
				}
				out.hang(gutter, gutter, p.markdown(block.body, body))
				continue
			}
			out.hang(gutter, gutter, p.block(block, body))
		}
	}
}

// journalItem renders milestones with a diamond and each answer below a link
// back to the question it answers. Unanswered-by-link groups keep the link
// visibly unavailable rather than pointing at a similar prompt.
func (v *liveActivityView) journalItem(out *conversationLines, journal *liveActivityJournal, index, width int, lead, indent string) {
	p := &v.painter
	body := width - ansi.StringWidth(lead)
	milestone := p.theme.Accent() + "◆" + liveActivityReset + " "
	answer := "• "
	if lead != "" {
		answer = ""
	}
	first := true
	gap := func() {
		if !first {
			out.add(0, strings.TrimRight(indent, " "))
		}
		first = false
	}
	for _, group := range journal.groups {
		if group.question == "" {
			for _, item := range group.answers {
				gap()
				rows := p.markdown(item.text, body-2)
				for k := range rows {
					if k == 0 {
						rows[k] = milestone + rows[k]
					} else {
						rows[k] = "  " + rows[k]
					}
				}
				out.hang(lead, indent, rows)
			}
			continue
		}
		gap()
		target := uint64(0)
		if question, ok := v.questionLink(group, index); ok {
			target = question.Seq
		}
		out.add(target, lead+v.questionLinkLabel(group, index, body))
		for _, item := range group.answers {
			rows := p.markdown(item.text, body-ansi.StringWidth(answer))
			for k := range rows {
				if k == 0 {
					rows[k] = answer + rows[k]
				} else {
					rows[k] = strings.Repeat(" ", ansi.StringWidth(answer)) + rows[k]
				}
			}
			out.hang(indent, indent, rows)
		}
	}
	tail := *journal
	tail.groups = nil
	if rows := p.journal(&tail, body, false); len(rows) > 0 {
		out.hang(indent, indent, rows)
	}
}

// questionLink is the retained entry an answer group links to, if loaded.
func (v *liveActivityView) questionLink(group liveActivityAnswerGroup, before int) (activityPaneEntry, bool) {
	if group.target != 0 {
		for _, entry := range v.entries[:before] {
			if entry.Seq == group.target {
				return entry, true
			}
		}
	}
	return activityPaneEntry{}, false
}

// questionLinkLabel names the linked prompt by kind and time. Main never
// repeats the question text itself.
func (v *liveActivityView) questionLinkLabel(group liveActivityAnswerGroup, before, width int) string {
	question, ok := v.questionLink(group, before)
	if !ok {
		return ansi.Truncate(liveActivityDim+"↩ re: an earlier message · not loaded"+liveActivityUndim, width, "…")
	}
	return v.linkLabel(question, width)
}

// linkLabel names a linked prompt by kind and time.
func (v *liveActivityView) linkLabel(question activityPaneEntry, width int) string {
	target := "your message"
	if question.Kind == "start" || question.Kind == "assignment" {
		target = "assignment"
	}
	label := v.painter.theme.Accent() + "↩ re: " + target + liveActivityReset + liveActivityDim + " " + question.Observed.Local().Format("15:04:05") + liveActivityUndim
	return ansi.Truncate(label, width, "…")
}
