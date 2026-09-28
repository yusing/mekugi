package router

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
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
// with one agent forms a thread. Main's reasoning between a thread's items
// neither breaks it nor enters its rail: it follows the thread instead. Main's
// tools never sit headless below agent traffic: the reasoning or commentary
// they continue moves below the traffic, or, when it already heads earlier
// tools, a continuation row names it. It reuses Activity's block parsing,
// painter and viewport logic.
func (v *liveActivityView) renderConversation(width int) liveActivityFeed {
	type item struct {
		first, last int
		agent       string // Thread agent, or "" when the item cannot join one.
		aside       bool   // Main reasoning, which neither starts nor ends a thread.
		lead        int    // Entry index of the Main item a tool group continues, or -1.
		attached    bool   // Tools branching from the reasoning directly above, with no gap.
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
			items = append(items, item{first: i, last: last, agent: v.threadAgent(i), aside: entry.Agent == "Main" && entry.Kind == "reasoning", lead: -1})
		}
		i = j
	}
	var feed liveActivityFeed
	v.questionRows = make(map[uint64]int)
	used := make(map[liveActivityRunKey]liveActivityRun)
	continues := func(a, b int) bool {
		return a >= 0 && b < len(items) && items[a].agent != "" && items[a].agent == items[b].agent
	}
	// Hold reasoning the thread continues past until the thread ends.
	ordered := make([]item, 0, len(items))
	var held []item
	previous := -1 // Latest item that is not an aside.
	for k, it := range items {
		next := k + 1
		for next < len(items) && items[next].aside {
			next++
		}
		switch {
		case it.aside && continues(previous, next):
			held = append(held, it)
			continue
		case !it.aside:
			previous = k
		}
		ordered = append(ordered, it)
		if !it.aside && !continues(k, next) {
			ordered, held = append(ordered, held...), held[:0]
		}
	}
	items = append(ordered, held...)
	traffic := func(it item) bool {
		entry := v.entries[it.first]
		return it.agent != "" || entry.Agent != "Main" && entry.Agent != "You"
	}
	lead, headed := -1, false // Latest lead's position, and whether tools follow it.
	for k := 0; k < len(items); k++ {
		switch entry := v.entries[items[k].first]; {
		case entry.Agent == "You":
			lead = -1
		case v.conversationLead(items[k].first):
			lead, headed = k, false
		case conversationTool(entry):
			if lead >= 0 && traffic(items[k-1]) {
				if !headed && !slices.ContainsFunc(items[lead+1:k], func(it item) bool { return !traffic(it) }) {
					moved := items[lead]
					copy(items[lead:k-1], items[lead+1:k])
					items[k-1], lead = moved, k-1
				} else {
					items[k].lead = items[lead].first
				}
			}
			headed = true
		}
	}
	// Reasoning heads the tools that directly follow it in display order.
	// Tools that continue it after agent traffic name it instead.
	for k := 1; k < len(items); k++ {
		previous := items[k-1]
		items[k].attached = items[k].lead < 0 && conversationTool(v.entries[items[k].first]) &&
			previous.aside && v.entries[previous.first].Agent == "Main" && v.conversationLead(previous.first)
	}
	start := 0 // Thread's first item.
	for k, it := range items {
		var thread conversationThread
		if continues(k-1, k) {
			thread = conversationThread{joined: true, first: items[start].first, previous: items[k-1].first}
		} else {
			start = k
		}
		thread.followed = continues(k, k+1)
		key := liveActivityRunKey{first: v.entries[it.first].Seq, last: v.entries[it.last].Seq, width: width, theme: v.painter.Theme, hover: -1, main: true, thread: thread}
		if it.lead >= 0 {
			key.lead = v.entries[it.lead].Seq
		}
		if v.snippet.run == key.first {
			key.hover = v.snippet.block
		}
		run, ok := v.runs[key]
		if !ok {
			run = v.conversationItem(it.first, it.last, width, thread)
			if it.lead >= 0 {
				run.lines = append([]string{v.continuation(it.lead, width)}, run.lines...)
				run.snippets = append([]liveActivitySnippet{{}}, run.snippets...)
				run.questions = append([]uint64{0}, run.questions...)
			}
		}
		used[key] = run
		if len(feed.lines) > 0 && !thread.joined && !it.attached {
			feed.lines = append(feed.lines, "")
			feed.heads = append(feed.heads, len(feed.lines)-1)
			feed.snippets = append(feed.snippets, liveActivitySnippet{})
			feed.questions = append(feed.questions, 0)
		}
		head := len(feed.lines)
		if entry := &v.entries[it.first]; entry.Agent == "Main" && entry.Kind == "text" && entry.journal == nil && strings.TrimSpace(entry.Text) != "" {
			feed.mainReply = entry
			feed.mainReplyStart, feed.mainReplyEnd = head, head+len(run.lines)
		}
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

// conversationLead reports Main reasoning or commentary, which heads the
// tools that follow it.
func (v *liveActivityView) conversationLead(index int) bool {
	entry, blocks := v.entries[index], v.blocks[index]
	switch {
	case entry.Agent != "Main" || entry.journal != nil:
		return false
	case entry.Kind == "reasoning":
		return len(blocks) > 0 && activityui.ReasoningSummaryBody(blocks[0].Body) != ""
	}
	return entry.Kind == "text" && strings.TrimSpace(entry.Text) != ""
}

// continuation is one row naming the lead a tool group resumes after agent
// traffic, so the tools cannot read as that traffic's.
func (v *liveActivityView) continuation(index, width int) string {
	entry, p := v.entries[index], &v.painter
	if entry.Kind == "text" {
		return conversationHeading(p.Theme.Accent()+"●"+activityui.Reset, "\x1b[1m"+p.Theme.Accent()+"main"+activityui.Reset, "continued", entry, width)
	}
	suffix := activityui.Dim + " · continued" + activityui.Undim
	row := p.Block(v.blocks[index][0], width)[0]
	return ansi.Truncate(row, max(0, width-ansi.StringWidth(suffix)), "…") + activityui.Reset + suffix
}

// conversationThread places an item in a thread: adjacent agent traffic with
// one agent, which shares a gutter instead of repeating headings and quotes.
type conversationThread struct {
	joined          bool // Continues the thread item above.
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
func trafficAgent(entry activityPaneEntry, block activityui.Block) string {
	agent := ""
	switch {
	case block.Kind == "start" || block.Kind == "message" && block.From == "/root":
		agent = block.To
	case block.Kind == "message":
		agent = block.From
	case block.Kind == "final" || block.Kind == "error":
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
	return entry.activitySeq != 0 && entry.Kind == "final" && len(blocks) == 1 && blocks[0].Journal != nil && len(blocks[0].Journal.Groups) == 0
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
	case entry.Agent == "Main" && entry.Kind == "progress":
		out.add(0, activityui.Wrap(activityui.Dim+"• "+livediff.Safe(entry.Text, false)+activityui.Undim, width, false)...)
	case entry.Agent == "Main" && entry.Kind == "reasoning" && first == last:
		for index, block := range blocks {
			// Settled provider thinking shows its header; a click toggles it.
			snippet := liveActivitySnippet{entry.Seq, index}
			toggle := v.collapseToggle(&block, snippet)
			for _, row := range p.Block(block, width) {
				out.add(0, row)
				if toggle {
					out.snippets[len(out.snippets)-1] = snippet
				}
			}
		}
	case entry.Agent == "You":
		v.userItem(&out, entry, width)
	case conversationTool(entry):
		var group []activityui.Block
		for k := first; k <= last; k++ {
			if v.visible(v.entries[k]) {
				group = append(group, v.blocks[k]...)
			}
		}
		// The operations form a tree under the reasoning above them. An edit
		// group's later rows and output notes continue their operation's branch.
		var parts [][]string
		var toggles []liveActivitySnippet // Each part's output toggle, if any.
		// Its connectors sit beneath the reasoning bullet.
		for index, block := range activityui.AlignVerbs(activityui.MergeEdits(activityui.GroupOperations(activityui.MergeLiveActivityReads(group)))) {
			snippet := liveActivitySnippet{entry.Seq, index}
			toggle := v.collapseToggle(&block, snippet)
			if len(parts) > 0 && (block.Kind == "filter" || block.GroupHeader != "" && !block.GroupStart) {
				parts[len(parts)-1] = append(parts[len(parts)-1], p.Block(block, width-2)...)
				continue
			}
			parts = append(parts, p.Block(block, width-2))
			toggles = append(toggles, liveActivitySnippet{})
			if toggle {
				toggles[len(toggles)-1] = snippet
			}
		}
		rows := activityui.Tree(parts)
		for i, part := range parts {
			for range part {
				out.add(0, rows[0])
				out.snippets[len(out.snippets)-1], rows = toggles[i], rows[1:]
			}
		}
	case entry.Agent == "Main" && entry.journal != nil && len(blocks) == 1 && blocks[0].Journal != nil:
		v.flushItem(&out, entry, blocks[0].Journal, first, width)
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
		out.hang(gutter, gutter, p.Markdown(livediff.Safe(entry.Text, false), width-2))
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
		head += " " + activityui.Dim + detail + activityui.Undim
	}
	stamp := activityui.Dim + entry.Observed.Local().Format("15:04:05") + activityui.Undim
	head = ansi.Truncate(head, width, "…")
	if gap := width - ansi.StringWidth(head) - ansi.StringWidth(stamp); gap >= 2 {
		head += strings.Repeat(" ", gap) + stamp
	}
	return head
}

// mainHeading and mainGutter mark Main's own replies the way agent traffic
// is marked, so every transcript item starts with who spoke and when.
func mainHeading(p *activityui.Painter, entry activityPaneEntry, width int) string {
	return conversationHeading(p.Theme.Accent()+"●"+activityui.Reset, "\x1b[1m"+p.Theme.Accent()+"main"+activityui.Reset, "", entry, width)
}

func mainGutter(p *activityui.Painter) string {
	return p.Theme.Accent() + "┃" + activityui.Reset + " "
}

// pinnedMainReply is a bounded copy, not a moved transcript item. The original
// remains scrollable in full, with its question link and chronological context.
func (v *liveActivityView) pinnedMainReply(width, height, rows int, feed liveActivityFeed) []string {
	budget := min(8, height/3)
	offset, _ := v.viewportPosition(feed, rows)
	if budget < 3 || feed.mainReply == nil || feed.mainReplyStart < offset+rows && feed.mainReplyEnd > offset {
		return nil
	}
	entry := *feed.mainReply
	p := &v.painter
	body := p.Markdown(livediff.Safe(entry.Text, false), max(1, width-2))
	limit := budget - 2 // Heading and separator leave room for scrolling activity.
	if len(body) > limit {
		body = body[:limit]
		body[limit-1] = ansi.Truncate(body[limit-1], max(0, width-3), "") + "…"
	}
	lines := []string{conversationHeading(p.Theme.Accent()+"●"+activityui.Reset, "main", "pinned", entry, width)}
	for _, line := range body {
		lines = append(lines, mainGutter(p)+line)
	}
	return append(lines, activityui.Dim+strings.Repeat("─", width)+activityui.Reset)
}

// milestoneItem labels journal milestones as such, under the accent gutter,
// so progress notes cannot be mistaken for Main's replies.
func (v *liveActivityView) milestoneItem(out *conversationLines, entry activityPaneEntry, milestones []string, width int) {
	accent := v.painter.Theme.Accent()
	out.add(0, conversationHeading(accent+"◆"+activityui.Reset, "\x1b[1m"+accent+"journal"+activityui.Reset, "", entry, width))
	gutter := accent + "│" + activityui.Reset + " "
	for _, text := range milestones {
		rows := v.painter.Markdown(text, width-4)
		if len(milestones) == 1 {
			rows = v.painter.Markdown(text, width-2)
			out.hang(gutter, gutter, rows)
			continue
		}
		out.hang(gutter+"• ", gutter+"  ", rows)
	}
}

// flushItem shows a terminal journal flush: its milestones as a journal
// block, then each answer as Main's reply below a link to its question.
func (v *liveActivityView) flushItem(out *conversationLines, entry activityPaneEntry, journal *activityui.Journal, index, width int) {
	var milestones []string
	answers := *journal
	answers.Groups = nil
	for _, group := range journal.Groups {
		if group.Question != "" {
			answers.Groups = append(answers.Groups, group)
			continue
		}
		for _, item := range group.Answers {
			milestones = append(milestones, item.Text)
		}
	}
	if len(milestones) > 0 {
		v.milestoneItem(out, entry, milestones, width)
		if len(answers.Groups) > 0 {
			out.add(0, "")
		}
	}
	if len(answers.Groups) > 0 || len(milestones) == 0 {
		out.add(0, mainHeading(&v.painter, entry, width))
		gutter := mainGutter(&v.painter)
		v.journalItem(out, &answers, index, width, gutter, gutter)
	}
}

func (v *liveActivityView) userItem(out *conversationLines, entry activityPaneEntry, width int) {
	band := userBand(v.painter.Theme)
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
		rows = activityui.Wrap(text.String(), width-2, true)
	} else {
		rows = v.painter.Markdown(livediff.Safe(entry.Text, false), width-2)
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	stamp := activityui.Dim + entry.Observed.Local().Format("15:04:05") + activityui.Undim
	for k, row := range rows {
		lead := "  "
		if k == 0 {
			lead = liveActivityPrompt + "❯" + activityui.Reset + " "
		}
		line := activityui.Reset + ansi.Truncate(lead+row, width, "…") + activityui.Reset
		if k == 0 && ansi.StringWidth(line)+2+ansi.StringWidth(stamp) <= width {
			line += strings.Repeat(" ", width-ansi.StringWidth(line)-ansi.StringWidth(stamp)) + stamp
		}
		if band != "" {
			line = strings.ReplaceAll(line, activityui.Reset, activityui.Reset+band)
			line = band + line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line))) + "\x1b[49m"
		}
		out.add(0, line)
	}
}

// agentItem gives agent traffic a one-line heading naming the agent and what
// happened, with the body under that agent's colored gutter. An item joined to
// a thread gets a connector heading instead, and skips quoting what the thread
// already shows above it.
func (v *liveActivityView) agentItem(out *conversationLines, entry activityPaneEntry, blocks []activityui.Block, index, width int, thread conversationThread) {
	p := &v.painter
	glyph, detail := activityui.Dim+"●"+activityui.Undim, ""
	var block activityui.Block
	if len(blocks) > 0 {
		block = blocks[0]
	}
	agent := trafficAgent(entry, block)
	if agent == "" {
		agent = entry.Agent
	}
	reply := block.From != "/root" && block.Kind != "start"
	switch {
	case block.Kind == "start":
		glyph, detail = activityui.Green+"▶"+activityui.Reset, "started"
		if block.Label != "" {
			detail += " · " + ansi.Strip(p.Inline(block.Label))
		}
	case block.Kind == "message" && block.From == "/root":
		glyph = activityui.Dim + "→" + activityui.Undim
		if thread.joined {
			detail = "follow-up"
		}
	case block.Kind == "message":
		glyph = activityui.Dim + "←" + activityui.Undim
		if thread.joined {
			detail = "replied"
		}
	case block.Kind == "final":
		glyph, detail = activityui.Green+"✓"+activityui.Reset, "finished"
	case block.Kind == "error":
		glyph = activityui.Red + "✗" + activityui.Reset
		if thread.joined {
			detail = "failed"
		}
	}
	color := activityui.Gutter(agent, p.Theme)
	if thread.joined {
		out.add(0, threadHeading(color+"├─"+activityui.Reset+glyph, detail, entry, v.entries[thread.previous], reply, width))
	} else {
		out.add(0, conversationHeading(glyph, p.Agent(agent), detail, entry, width))
	}
	gutter := color + "│" + activityui.Reset + " "
	tail, limit := color+"╰─"+activityui.Reset, conversationLatestRows
	if thread.followed {
		tail, limit = gutter, conversationEarlierRows
	}
	body := width - 2
	switch {
	case block.Kind == "start" || block.Kind == "message":
		switch {
		case entry.activitySeq != 0 && block.From != "/root":
			v.replyExcerpt(out, entry, block.Body, gutter, tail, body, limit)
		case thread.followed:
			v.collapsedItem(out, entry, p.Markdown(block.Body, body), gutter, body)
		default:
			out.hang(gutter, gutter, p.Markdown(block.Body, body))
		}
	case block.Kind == "final" && block.Journal != nil:
		if entry.activitySeq == 0 {
			v.journalItem(out, block.Journal, index, width, gutter, gutter)
		} else {
			// A completion is one excerpt, even when its journal contains
			// several answers. Activity retains the complete result.
			for _, group := range slices.Backward(block.Journal.Groups) {
				if len(group.Answers) == 0 {
					continue
				}
				if group.Question != "" && !v.threadTask(thread, index, group.Target) {
					v.journalReplyContext(out, group, index, gutter, body)
				}
				v.replyExcerpt(out, entry, group.Answers[len(group.Answers)-1].Text, gutter, tail, body, limit)
				break
			}
		}
	default:
		for _, block := range blocks {
			if block.Kind == "error" {
				out.hang(gutter, gutter, activityui.Wrap(activityui.Red+block.Body+activityui.Reset, body, false))
				continue
			}
			if block.Kind == "final" {
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
					v.replyExcerpt(out, entry, block.Body, gutter, tail, body, limit)
				} else {
					out.hang(gutter, gutter, p.Markdown(block.Body, body))
				}
				continue
			}
			out.hang(gutter, gutter, p.Block(block, body))
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
		head += " " + activityui.Dim + detail + activityui.Undim
	}
	head = ansi.Truncate(head, width, "…")
	if !reply {
		stamp := activityui.Dim + entry.Observed.Local().Format("15:04:05") + activityui.Undim
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
	header := v.painter.Theme.Accent() + "↩ re: " + target + activityui.Reset
	if question.Seq == 0 {
		header = activityui.Dim + "↩ re: original message (not loaded)" + activityui.Undim
	} else if !question.Observed.IsZero() {
		header += activityui.Dim + " " + question.Observed.Local().Format("15:04:05") + activityui.Undim
	}
	out.add(question.Seq, gutter+ansi.Truncate(header, width, "…"))
	rows, _ := liveActivityExcerpt(v.painter.Quote(livediff.Safe(text, false), width), width, 2)
	for _, row := range rows {
		out.add(question.Seq, gutter+row)
	}
}

func (v *liveActivityView) journalReplyContext(out *conversationLines, group activityui.AnswerGroup, index int, gutter string, width int) {
	question, ok := v.questionLink(group, index)
	if !ok {
		question = activityPaneEntry{Text: group.Question}
	}
	v.replyContext(out, question, gutter, width)
}

// replyExcerpt keeps up to limit rows of a reply above a link to the whole
// reply in Activity, which counts the rows left out. tail leads the link row.
func (v *liveActivityView) replyExcerpt(out *conversationLines, entry activityPaneEntry, text, gutter, tail string, width, limit int) {
	rows, hidden := liveActivityExcerpt(v.painter.Markdown(text, width), width, limit)
	out.hang(gutter, gutter, rows)
	link := v.painter.Theme.Accent() + "↩ Open reply in Activity" + activityui.Reset
	if hidden > 0 {
		link += activityui.Dim + " · " + activityui.MoreLines(hidden) + activityui.Undim
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
		hint := activityui.MoreLines(hidden)
		if v.snippet == snippet {
			hint = activityui.Underline(hint)
		}
		room := width - ansi.StringWidth(hint) - 1
		rows, _ = liveActivityExcerpt(rows, room, conversationEarlierRows)
		last := &rows[len(rows)-1]
		*last += strings.Repeat(" ", max(1, width-ansi.StringWidth(*last)-ansi.StringWidth(hint))) + activityui.Dim + hint + activityui.Undim
	}
	for _, row := range rows {
		out.add(0, gutter+row)
		if hidden > 0 {
			out.snippets[len(out.snippets)-1] = snippet
		}
	}
}

// liveActivityExcerpt keeps the first limit rows with content and counts the
// content rows left out. Paragraph gaps and empty quote rows are skipped so a
// truncated excerpt always ends its last visible text row with the ellipsis
// rather than leaving it on a row alone.
func liveActivityExcerpt(rows []string, width, limit int) ([]string, int) {
	var kept []string
	hidden := 0
	for _, row := range rows {
		if activityui.BlankRow(row) {
			continue
		}
		if len(kept) < limit {
			kept = append(kept, row)
			continue
		}
		if hidden == 0 {
			kept[limit-1] = ansi.Truncate(kept[limit-1], max(0, width-1), "") + activityui.Dim + "…" + activityui.Undim
		}
		hidden++
	}
	return kept, hidden
}

// journalItem renders milestones with a diamond and each answer below a link
// back to the question it answers. Unanswered-by-link groups keep the link
// visibly unavailable rather than pointing at a similar prompt.
func (v *liveActivityView) journalItem(out *conversationLines, journal *activityui.Journal, index, width int, lead, indent string) {
	p := &v.painter
	body := width - ansi.StringWidth(lead)
	milestone := p.Theme.Accent() + "◆" + activityui.Reset + " "
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
	for _, group := range journal.Groups {
		if group.Question == "" {
			for _, item := range group.Answers {
				gap()
				rows := p.Markdown(item.Text, body-2)
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
		for _, item := range group.Answers {
			rows := p.Markdown(item.Text, body-ansi.StringWidth(answer))
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
	tail.Groups = nil
	if rows := p.Journal(&tail, body, false); len(rows) > 0 {
		out.hang(indent, indent, rows)
	}
}

// questionLink is the retained entry an answer group links to, if loaded.
func (v *liveActivityView) questionLink(group activityui.AnswerGroup, before int) (activityPaneEntry, bool) {
	if group.Target != 0 {
		for _, entry := range v.entries[:before] {
			if entry.Seq == group.Target {
				return entry, true
			}
		}
	}
	return activityPaneEntry{}, false
}
