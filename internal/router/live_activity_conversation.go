package router

import (
	"fmt"
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
// author headings, and agent traffic under one-line headings. Adjacent traffic
// with one agent forms a thread, which Main's reasoning summaries continue
// through. It reuses Activity's block parsing, painter and viewport logic.
func (v *liveActivityView) renderConversation(width int) liveActivityFeed {
	type item struct {
		first, last int
		agent       string // Thread agent, or "" when the item cannot join one.
		aside       bool   // Main reasoning, which neither starts nor ends a thread.
	}
	var items []item
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
		if !v.conversationEmpty(i) {
			entry := v.entries[i]
			items = append(items, item{i, last, v.threadAgent(i), entry.Agent == "Main" && entry.Kind == "reasoning"})
		}
		i = j
	}
	var feed liveActivityFeed
	v.questionRows = make(map[uint64]int)
	used := make(map[liveActivityRunKey]liveActivityRun)
	continues := func(a, b int) bool {
		return a >= 0 && b < len(items) && items[a].agent != "" && items[a].agent == items[b].agent
	}
	start, previous := 0, -1 // Thread's first item, and the latest item that is not an aside.
	for k, it := range items {
		next := k + 1
		for next < len(items) && items[next].aside {
			next++
		}
		var thread conversationThread
		switch {
		case it.aside:
			if continues(previous, next) {
				thread = conversationThread{joined: true, first: items[start].first, previous: items[previous].first, followed: true}
			}
		case continues(previous, k):
			thread = conversationThread{joined: true, first: items[start].first, previous: items[previous].first}
		default:
			start = k
		}
		if !it.aside {
			thread.followed, previous = continues(k, next), k
		}
		key := liveActivityRunKey{v.entries[it.first].Seq, v.entries[it.last].Seq, width, 0, v.painter.theme, -1, true, thread}
		if v.snippet.run == key.first {
			key.hover = v.snippet.block
		}
		run, ok := v.runs[key]
		if !ok {
			run = v.conversationItem(it.first, it.last, width, thread)
		}
		used[key] = run
		if len(feed.lines) > 0 && !thread.joined {
			feed.lines = append(feed.lines, "")
			feed.heads = append(feed.heads, len(feed.lines)-1)
			feed.snippets = append(feed.snippets, liveActivitySnippet{})
			feed.questions = append(feed.questions, 0)
		}
		head := len(feed.lines)
		if entry := v.entries[it.first]; entry.Agent == "You" || entry.Kind == "start" || entry.Kind == "assignment" {
			v.questionRows[entry.Seq] = head
		}
		for range run.lines {
			feed.heads = append(feed.heads, head)
		}
		feed.lines = append(feed.lines, run.lines...)
		feed.snippets = append(feed.snippets, run.snippets...)
		feed.questions = append(feed.questions, run.questions...)
	}
	v.runs = used
	return feed
}

// conversationThread places an item in a thread: adjacent agent traffic with
// one agent, which shares a gutter instead of repeating headings and quotes.
type conversationThread struct {
	joined          bool // Continues the thread item above, past any reasoning.
	first, previous int  // Entry indexes of the thread's first item and the item above, when joined.
	followed        bool // A later item continues the thread.
}

// Excerpt budgets: an item its thread has moved past keeps a short excerpt,
// while the latest reply keeps more, since it is what the transcript awaits.
const (
	conversationEarlierRows = 2
	conversationLatestRows  = 4
)

// threadAgent names the agent an item of agent traffic concerns, or "" when
// the item cannot join a thread.
func (v *liveActivityView) threadAgent(index int) string {
	entry := v.entries[index]
	if entry.Agent == "You" || entry.Agent == "Main" && entry.Kind != "start" && entry.Kind != "assignment" || len(v.blocks[index]) == 0 {
		return ""
	}
	return trafficAgent(entry, v.blocks[index][0])
}

// trafficAgent is the recipient of Main's assignments and messages, and
// otherwise the agent that sent or finished the traffic.
func trafficAgent(entry activityPaneEntry, block liveActivityBlock) string {
	agent := ""
	switch {
	case block.kind == "start" || block.kind == "message" && block.from == "/root":
		agent = block.to
	case block.kind == "message":
		agent = block.from
	case block.kind == "final" || block.kind == "error":
	default:
		return ""
	}
	if agent == "" {
		agent = entry.Agent
	}
	return agent
}

// threadTask reports whether seq is the latest assignment in the thread above
// index. A reply to it needs no quote, but a reply to an earlier assignment
// keeps one so it does not read as answering the nearer task.
func (v *liveActivityView) threadTask(thread conversationThread, index int, seq uint64) bool {
	if !thread.joined || seq == 0 {
		return false
	}
	for _, entry := range slices.Backward(v.entries[thread.first:index]) {
		if entry.Kind == "start" || entry.Kind == "assignment" {
			return entry.Seq == seq
		}
	}
	return false
}

// conversationEmpty reports a completion whose Activity excerpt has no answer.
func (v *liveActivityView) conversationEmpty(index int) bool {
	entry, blocks := v.entries[index], v.blocks[index]
	return entry.activitySeq != 0 && entry.Kind == "final" && len(blocks) == 1 && blocks[0].journal != nil && len(blocks[0].journal.groups) == 0
}

type conversationLines struct {
	lines     []string
	questions []uint64
	snippets  []liveActivitySnippet
}

func (c *conversationLines) add(target uint64, lines ...string) {
	for _, line := range lines {
		c.lines = append(c.lines, line)
		c.questions = append(c.questions, target)
		c.snippets = append(c.snippets, liveActivitySnippet{})
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

func (v *liveActivityView) conversationItem(first, last, width int, thread conversationThread) liveActivityRun {
	entry := v.entries[first]
	blocks := v.blocks[first]
	if v.conversationEmpty(first) {
		return liveActivityRun{}
	}
	var out conversationLines
	p := &v.painter
	switch {
	case entry.Agent == "Main" && entry.Kind == "reasoning" && thread.joined:
		// A summary between a thread's items keeps the thread's rail.
		rail := liveAgentGutter(v.threadAgent(thread.first), p.theme) + "│" + liveActivityReset
		out.add(0, rail)
		for _, block := range blocks {
			out.hang(rail+" ", rail+" ", p.block(block, width-2))
		}
		out.add(0, rail)
	case entry.Agent == "Main" && entry.Kind == "reasoning":
		for _, block := range blocks {
			out.add(0, p.block(block, width)...)
		}
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
		if entry.native != nil && entry.native.question != 0 {
			for _, question := range v.entries[:first] {
				if question.Seq == entry.native.question {
					v.replyContext(&out, question, gutter, width-2)
					break
				}
			}
		}
		out.hang(gutter, gutter, p.markdown(livediff.Safe(entry.Text, false), width-2))
	default:
		v.agentItem(&out, entry, blocks, first, width, thread)
	}
	return liveActivityRun{lines: out.lines, snippets: out.snippets, questions: out.questions}
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
	var rows []string
	if entry.native != nil && len(entry.native.images) > 0 {
		// Keep attachment-bearing input literal, like the composer: Markdown
		// syntax must not consume an attachment as a link or code delimiter.
		var text strings.Builder
		at := 0
		for _, image := range entry.native.images {
			text.WriteString(livediff.Safe(entry.Text[at:image.start], false))
			text.WriteString("\x1b[1;36m")
			text.WriteString(livediff.Safe(entry.Text[image.start:image.end], false))
			text.WriteString("\x1b[22;39m")
			at = image.end
		}
		text.WriteString(livediff.Safe(entry.Text[at:], false))
		rows = liveActivityWrap(text.String(), width-2, true)
	} else {
		rows = v.painter.markdown(livediff.Safe(entry.Text, false), width-2)
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	stamp := liveActivityDim + entry.Observed.Local().Format("15:04:05") + liveActivityUndim
	for k, row := range rows {
		lead := "  "
		if k == 0 {
			lead = liveActivityPrompt + "❯" + liveActivityReset + " "
		}
		line := liveActivityReset + ansi.Truncate(lead+row, width, "…") + liveActivityReset
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
// happened, with the body under that agent's colored gutter. An item joined to
// a thread gets a connector heading instead, and skips quoting what the thread
// already shows above it.
func (v *liveActivityView) agentItem(out *conversationLines, entry activityPaneEntry, blocks []liveActivityBlock, index, width int, thread conversationThread) {
	p := &v.painter
	glyph, detail := liveActivityDim+"●"+liveActivityUndim, ""
	var block liveActivityBlock
	if len(blocks) > 0 {
		block = blocks[0]
	}
	agent := trafficAgent(entry, block)
	if agent == "" {
		agent = entry.Agent
	}
	reply := block.from != "/root" && block.kind != "start"
	switch {
	case block.kind == "start":
		glyph, detail = liveActivityGreen+"▶"+liveActivityReset, "started"
		if block.label != "" {
			detail += " · " + ansi.Strip(p.inline(block.label))
		}
	case block.kind == "message" && block.from == "/root":
		glyph = liveActivityDim + "→" + liveActivityUndim
		if thread.joined {
			detail = "follow-up"
		}
	case block.kind == "message":
		glyph = liveActivityDim + "←" + liveActivityUndim
		if thread.joined {
			detail = "replied"
		}
	case block.kind == "final":
		glyph, detail = liveActivityGreen+"✓"+liveActivityReset, "finished"
	case block.kind == "error":
		glyph = liveActivityRed + "✗" + liveActivityReset
		if thread.joined {
			detail = "failed"
		}
	}
	color := liveAgentGutter(agent, p.theme)
	if thread.joined {
		out.add(0, threadHeading(color+"├─"+liveActivityReset+glyph, detail, entry, v.entries[thread.previous], reply, width))
	} else {
		out.add(0, conversationHeading(glyph, p.agent(agent), detail, entry, width))
	}
	gutter := color + "│" + liveActivityReset + " "
	tail, limit := color+"╰─"+liveActivityReset, conversationLatestRows
	if thread.followed {
		tail, limit = gutter, conversationEarlierRows
	}
	body := width - 2
	switch {
	case block.kind == "start" || block.kind == "message":
		switch {
		case entry.activitySeq != 0 && block.from != "/root":
			v.replyExcerpt(out, entry, block.body, gutter, tail, body, limit)
		case thread.followed:
			v.collapsedItem(out, entry, p.markdown(block.body, body), gutter, body)
		default:
			out.hang(gutter, gutter, p.markdown(block.body, body))
		}
	case block.kind == "final" && block.journal != nil:
		if entry.activitySeq == 0 {
			v.journalItem(out, block.journal, index, width, gutter, gutter)
		} else {
			// A completion is one excerpt, even when its journal contains
			// several answers. Activity retains the complete result.
			for _, group := range slices.Backward(block.journal.groups) {
				if len(group.answers) == 0 {
					continue
				}
				if group.question != "" && !v.threadTask(thread, index, group.target) {
					v.journalReplyContext(out, group, index, gutter, body)
				}
				v.replyExcerpt(out, entry, group.answers[len(group.answers)-1].text, gutter, tail, body, limit)
				break
			}
		}
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
						if !v.threadTask(thread, index, question.Seq) {
							v.replyContext(out, question, gutter, body)
						}
						break
					}
				}
				if entry.activitySeq != 0 {
					v.replyExcerpt(out, entry, block.body, gutter, tail, body, limit)
				} else {
					out.hang(gutter, gutter, p.markdown(block.body, body))
				}
				continue
			}
			out.hang(gutter, gutter, p.block(block, body))
		}
	}
}

// threadHeading continues a thread from the item above. A reply shows how
// long the agent took rather than a second clock time.
func threadHeading(lead, detail string, entry, previous activityPaneEntry, reply bool, width int) string {
	if reply && !entry.Observed.IsZero() && !previous.Observed.IsZero() {
		detail = strings.TrimPrefix(detail+" · "+liveActivityAge(entry.Observed.Sub(previous.Observed)), " · ")
	}
	head := lead
	if detail != "" {
		head += " " + liveActivityDim + detail + liveActivityUndim
	}
	head = ansi.Truncate(head, width, "…")
	if !reply {
		stamp := liveActivityDim + entry.Observed.Local().Format("15:04:05") + liveActivityUndim
		if gap := width - ansi.StringWidth(head) - ansi.StringWidth(stamp); gap >= 2 {
			head += strings.Repeat(" ", gap) + stamp
		}
	}
	return head
}

// replyContext gives every reply the same header and separately quoted prompt.
// Both the header and excerpt navigate to the retained original, when loaded.
func (v *liveActivityView) replyContext(out *conversationLines, question activityPaneEntry, gutter string, width int) {
	target := "your message"
	if question.Kind == "start" || question.Kind == "assignment" {
		target = "assignment"
	}
	text := question.Text
	if question.assignment != nil {
		text = question.assignment.text
	}
	header := v.painter.theme.Accent() + "↩ re: " + target + liveActivityReset
	if question.Seq == 0 {
		header = liveActivityDim + "↩ re: original message (not loaded)" + liveActivityUndim
	} else if !question.Observed.IsZero() {
		header += liveActivityDim + " " + question.Observed.Local().Format("15:04:05") + liveActivityUndim
	}
	out.add(question.Seq, gutter+ansi.Truncate(header, width, "…"))
	rows, _ := liveActivityExcerpt(v.painter.quote(livediff.Safe(text, false), width), width, 2)
	for _, row := range rows {
		out.add(question.Seq, gutter+row)
	}
}

func (v *liveActivityView) journalReplyContext(out *conversationLines, group liveActivityAnswerGroup, index int, gutter string, width int) {
	question, ok := v.questionLink(group, index)
	if !ok {
		question = activityPaneEntry{Text: group.question}
	}
	v.replyContext(out, question, gutter, width)
}

// replyExcerpt keeps up to limit rows of a reply above a link to the whole
// reply in Activity, which counts the rows left out. tail leads the link row.
func (v *liveActivityView) replyExcerpt(out *conversationLines, entry activityPaneEntry, text, gutter, tail string, width, limit int) {
	rows, hidden := liveActivityExcerpt(v.painter.markdown(text, width), width, limit)
	out.hang(gutter, gutter, rows)
	link := v.painter.theme.Accent() + "↩ Open reply in Activity" + liveActivityReset
	if hidden > 0 {
		link += liveActivityDim + " · " + moreLines(hidden) + liveActivityUndim
	}
	out.add(entry.Seq, tail+ansi.Truncate(link, width, "…"))
}

// collapsedItem shortens an item its thread has moved past. Assignments have
// no Activity entry to open, so a click expands the item in place and another
// collapses it, as Activity does with long blocks.
func (v *liveActivityView) collapsedItem(out *conversationLines, entry activityPaneEntry, rows []string, gutter string, width int) {
	snippet := liveActivitySnippet{entry.Seq, 0}
	_, hidden := liveActivityExcerpt(rows, width, conversationEarlierRows)
	if hidden > 0 && !v.expanded[snippet] {
		hint := moreLines(hidden)
		if v.snippet == snippet {
			hint = "\x1b[4m" + hint + "\x1b[24m"
		}
		room := width - ansi.StringWidth(hint) - 1
		rows, _ = liveActivityExcerpt(rows, room, conversationEarlierRows)
		last := &rows[len(rows)-1]
		*last += strings.Repeat(" ", max(1, width-ansi.StringWidth(*last)-ansi.StringWidth(hint))) + liveActivityDim + hint + liveActivityUndim
	}
	for _, row := range rows {
		out.add(0, gutter+row)
		if hidden > 0 {
			out.snippets[len(out.snippets)-1] = snippet
		}
	}
}

func moreLines(n int) string {
	if n == 1 {
		return "+1 line"
	}
	return fmt.Sprintf("+%d lines", n)
}

// liveActivityExcerpt keeps the first limit rows with content and counts the
// content rows left out. Paragraph gaps and empty quote rows are skipped so a
// truncated excerpt always ends its last visible text row with the ellipsis
// rather than leaving it on a row alone.
func liveActivityExcerpt(rows []string, width, limit int) ([]string, int) {
	var kept []string
	hidden := 0
	for _, row := range rows {
		if strings.Trim(ansi.Strip(row), " │") == "" {
			continue
		}
		if len(kept) < limit {
			kept = append(kept, row)
			continue
		}
		if hidden == 0 {
			kept[limit-1] = ansi.Truncate(kept[limit-1], max(0, width-1), "") + liveActivityDim + "…" + liveActivityUndim
		}
		hidden++
	}
	return kept, hidden
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
		v.journalReplyContext(out, group, index, lead, body)
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
