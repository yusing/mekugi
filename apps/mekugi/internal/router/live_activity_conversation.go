package router

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// liveActivityPrompt marks the user's text in the composer and transcript.
const liveActivityPrompt = "\x1b[38;2;196;167;231m"

// userBand tints the user's prompts so turns are easy to find while scrolling.
// An undetected theme keeps the terminal background rather than guessing.
func userBand(theme diffrender.Theme) string {
	switch theme {
	case diffrender.LightTheme:
		return "\x1b[48;2;240;238;246m"
	case diffrender.DarkTheme:
		return "\x1b[48;2;34;32;42m"
	}
	return ""
}

func (v *liveActivityView) conversationSpeaker() string {
	if v.conversation && v.childrenOnly && v.only {
		return v.selected
	}
	return "Main"
}

// conversationTool groups the displayed speaker's operations so adjacent reads merge.
func (v *liveActivityView) conversationTool(entry activityPaneEntry) bool {
	return entry.Agent == v.conversationSpeaker() && slices.Contains([]string{"tool", "command", "hook", "attachments", "question"}, entry.Kind)
}

// renderConversation lays messages out as a transcript rather than an activity
// feed: prompts on a tinted band, Main's own messages without
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
	items := make([]item, 0, len(v.runs))
	// A question keeps its own rows without preventing operation groups on
	// either side from folding out of view.
	tools := func(entry activityPaneEntry) bool {
		return v.conversationTool(entry) && entry.Kind != "question"
	}
	for i := 0; i < len(v.Entries); {
		if !v.visible(v.Entries[i].Source) {
			i++
			continue
		}
		last, j := i, i+1
		// Codex can report consecutive steers as separate user items even
		// though they form one uninterrupted input in the transcript.
		if first := v.Entries[i].Source; first.Agent == "You" && first.native != nil && first.native.turn != "" {
			for ; j < len(v.Entries); j++ {
				next := v.Entries[j].Source
				if !v.visible(next) || next.Agent != "You" || next.native == nil || next.native.thread != first.native.thread || next.native.turn != first.native.turn {
					break
				}
				last = j
			}
		}
		if v.Entries[i].Source.Agent == v.conversationSpeaker() && v.Entries[i].Source.Kind == "reasoning" {
			for ; j < len(v.Entries); j++ {
				next := v.Entries[j].Source
				if !v.visible(next) {
					continue
				}
				if next.Agent != v.conversationSpeaker() || next.Kind != "reasoning" {
					break
				}
				last = j
			}
		}
		for _, same := range []func(activityPaneEntry) bool{tools} {
			if !same(v.Entries[i].Source) {
				continue
			}
			for ; j < len(v.Entries) && (!v.visible(v.Entries[j].Source) || same(v.Entries[j].Source)); j++ {
				if v.visible(v.Entries[j].Source) {
					last = j
				}
			}
			break
		}
		entry := v.Entries[i].Source
		items = append(items, item{first: i, last: last, agent: v.threadAgent(i), aside: entry.Agent == v.conversationSpeaker() && entry.Kind == "reasoning", lead: -1})
		i = j
	}
	// Warm runs already know the row count. Reserve the parallel row slices
	// together instead of repeatedly copying the whole transcript as they grow.
	rowCapacity := len(items) // Allow separators and a changing tail.
	for key, run := range v.runs {
		if key.expansion == 0 || key.expansion == v.expansion {
			rowCapacity += len(run.lines)
		}
	}
	feed := liveActivityFeed{
		lines:     make([]string, 0, rowCapacity),
		heads:     make([]int, 0, rowCapacity),
		snippets:  make([]liveActivitySnippet, 0, rowCapacity),
		questions: make([]uint64, 0, rowCapacity),
	}
	v.questionRows = make(map[uint64]int)
	used := make(map[liveActivityRunKey]liveActivityRun, len(items))
	continues := func(a, b int) bool {
		return a >= 0 && b < len(items) && items[a].agent != "" && items[a].agent == items[b].agent
	}
	// Hold asides the thread continues past until the thread ends.
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
		entry := v.Entries[it.first].Source
		return it.agent != "" || entry.Agent != v.conversationSpeaker() && entry.Agent != "You"
	}
	lead, headed := -1, false // Latest lead's position, and whether tools follow it.
	for k := 0; k < len(items); k++ {
		switch entry := v.Entries[items[k].first].Source; {
		case entry.Agent == "You":
			lead = -1
		case v.conversationLead(items[k].first):
			lead, headed = k, false
		case v.conversationTool(entry):
			if lead >= 0 && traffic(items[k-1]) {
				if !headed && !slices.ContainsFunc(items[lead+1:k], func(it item) bool { return !traffic(it) }) {
					moved := items[lead]
					copy(items[lead:k-1], items[lead+1:k])
					items[k-1], lead = moved, k-1
				} else {
					items[k].lead = items[lead].last
				}
			}
			headed = true
		}
	}
	// Reasoning heads the tools that directly follow it in display order.
	// Tools that continue it after agent traffic name it instead.
	for k := 1; k < len(items); k++ {
		previous := items[k-1]
		items[k].attached = items[k].lead < 0 && v.conversationTool(v.Entries[items[k].first].Source) &&
			previous.aside && v.Entries[previous.first].Source.Agent == v.conversationSpeaker() && v.conversationLead(previous.first)
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
		key := liveActivityRunKey{first: v.Entries[it.first].Source.Seq, last: v.Entries[it.last].Source.Seq, revision: v.RunRevision(it.first, it.last+1), width: width, theme: v.painter.Theme, hover: -1, main: true, thread: thread, excerpt: v.excerpt(v.passed[v.Entries[it.first].Source.Seq]), tail: v.tailRows(), expansion: v.expansion}
		if v.Entries[it.first].Source.Agent == "You" {
			key.expansion, key.excerpt = 0, false
		}
		if it.lead >= 0 {
			key.lead = v.Entries[it.lead].Source.Seq
		}
		if v.snippet.run == key.first {
			key.hover = v.snippet.block
		}
		run, ok := v.runs[key]
		if ok && !run.colored && !v.painter.LayoutOnly {
			ok = false
		}
		for k := it.first; k <= it.last; k++ {
			if n := v.Entries[k].Source.native; n != nil && n.running {
				ok = false
				break
			}
		}
		var render func() liveActivityRun
		if !ok || !run.colored {
			render = func() liveActivityRun {
				window := v.syntaxWindow
				if window != nil && it.lead >= 0 {
					v.syntaxWindow = &liveActivitySyntaxWindow{window.first - 1, window.last - 1}
				}
				run := v.conversationItem(it.first, it.last, width, thread)
				v.syntaxWindow = window
				if it.lead >= 0 {
					continuation := v.paintBlock(0, 1, func() []string { return []string{v.continuation(it.lead, width)} })
					run.lines = append(continuation, run.lines...)
					run.snippets = append([]liveActivitySnippet{{}}, run.snippets...)
					run.questions = append([]uint64{0}, run.questions...)
					for seq, row := range run.entryRows {
						run.entryRows[seq] = row + 1
					}
				}
				return run
			}
		}
		if !ok {
			run = render()
			run.colored = !v.painter.LayoutOnly
		}
		used[key] = run
		if len(feed.lines) > 0 && !thread.joined && !it.attached {
			feed.separator()
		}
		head := len(feed.lines)
		if !run.colored {
			if feed.paints == nil {
				feed.paints = make([]liveActivityPaint, 0, len(items)-k)
			}
			feed.paints = append(feed.paints, liveActivityPaint{head, key, render})
		}
		// A sent message or batch shrinking above a scrolled viewport keeps its rows still.
		if full := key; !ok && key.excerpt && !v.following && head < v.offset {
			full.excerpt = false
			if shown, ok := v.runs[full]; ok {
				v.offset -= len(shown.lines) - len(run.lines)
			}
		}
		for seq, row := range run.entryRows {
			v.questionRows[seq] = head + row
		}
		if entry := v.Entries[it.first].Source; entry.Kind == "start" || entry.Kind == "assignment" {
			v.questionRows[entry.Seq] = head
		}
		owner := it.first
		for row := range run.lines {
			start := head
			if v.Entries[it.first].Source.Agent == "You" {
				for owner < it.last && run.entryRows[v.Entries[owner+1].Source.Seq] <= row {
					owner++
				}
				start += run.entryRows[v.Entries[owner].Source.Seq]
			}
			feed.heads = append(feed.heads, start)
		}
		feed.appendRows(run)
		feed.spans = append(feed.spans, liveActivitySpan{key.first, head, len(feed.lines)})
		if v.sentMessage(it.first) || !key.excerpt && run.batch {
			feed.passing = append(feed.passing, liveActivityPassing{key.first, len(feed.lines)})
		}
	}
	for _, entry := range v.Entries {
		if entry.Source.Kind == "question_reply" && entry.Source.native != nil {
			if row, ok := v.questionRows[entry.Source.native.question]; ok {
				v.questionRows[entry.Source.Seq] = row
			}
		}
	}
	v.retainRuns(used)
	return feed
}

// sentMessage reports a spawn, follow-up or Main message with an Activity
// entry, which becomes an excerpt once it has scrolled out of view.
func (v *liveActivityView) sentMessage(index int) bool {
	entry, blocks := v.Entries[index].Source, v.Entries[index].Values
	if entry.activitySeq == 0 || len(blocks) == 0 {
		return false
	}
	return blocks[0].Kind == "start" || entry.Kind == "assignment" || blocks[0].Kind == "message" && blocks[0].From == "/root"
}

// conversationLead reports Main reasoning or commentary, which heads the
// tools that follow it.
func (v *liveActivityView) conversationLead(index int) bool {
	entry, blocks := v.Entries[index].Source, v.Entries[index].Values
	switch {
	case entry.Agent != v.conversationSpeaker():
		return false
	case entry.Kind == "reasoning":
		return len(blocks) > 0 && activityui.ReasoningSummaryBody(blocks[0].Body) != ""
	}
	return entry.Kind == "text" && strings.TrimSpace(entry.Text) != ""
}

// continuation is one row naming the lead a tool group resumes after agent
// traffic, so the tools cannot read as that traffic's.
func (v *liveActivityView) continuation(index, width int) string {
	entry, p := v.Entries[index].Source, &v.painter
	if entry.Kind == "text" {
		return conversationHeading(p.Theme.Accent()+"●"+terminalui.Reset, "\x1b[1m"+p.Theme.Accent()+"main"+terminalui.Reset, "continued", entry, width)
	}
	suffix := terminalui.Dim + " · continued" + terminalui.Undim
	row := p.Block(v.blocks(index)[0], width)[0]
	return ansi.Truncate(row, max(0, width-ansi.StringWidth(suffix)), "…") + terminalui.Reset + suffix
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

// conversationSourceRows bounds a command or program preview in Main until
// the reader opens it; Activity keeps its own per-block clip.
const conversationSourceRows = 5

// threadAgent names the agent an item of agent traffic concerns, or "" when
// the item cannot join a thread.
func (v *liveActivityView) threadAgent(index int) string {
	entry := v.Entries[index].Source
	if entry.Agent == "You" || entry.Agent == v.conversationSpeaker() && entry.Kind != "start" && entry.Kind != "assignment" || len(v.Entries[index].Values) == 0 {
		return ""
	}
	return trafficAgent(entry, v.blocks(index)[0])
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
	for _, entry := range slices.Backward(v.Entries[thread.first:index]) {
		if entry.Source.Kind == "start" || entry.Source.Kind == "assignment" {
			return entry.Source.Seq == seq
		}
	}
	return false
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
	entry := v.Entries[first].Source
	v.painter.CopyScope = entry.Seq
	blocks := v.blocks(first)
	var out conversationLines
	var laid []activityui.Block // Tool blocks, indexed by the snippets naming them.
	batch := false
	entryRows := make(map[uint64]int)
	p := &v.painter
	switch {
	case entry.Agent == v.conversationSpeaker() && (entry.Kind == "reasoning" || entry.Kind == "progress"):
		var summaries []activityui.Block
		for k := first; k <= last; k++ {
			if v.visible(v.Entries[k].Source) {
				for _, block := range v.blocks(k) {
					block.Source = v.Entries[k].Source.Seq
					summaries = append(summaries, block)
				}
			}
		}
		if v.expansion == 0 {
			summaries = activityui.CompactReasoning(summaries)
		}
		laid = summaries
		for index, block := range laid {
			block = v.discloseBlock(block)
			// Thinking and reset disclosures share the existing output dialog.
			snippet := liveActivitySnippet{run: entry.Seq, block: index}
			toggle := v.clickTarget(&block, snippet, width)
			for _, row := range v.paintBlock(len(out.lines), 0, func() []string { return p.Block(v.outputContext(block), width) }) {
				out.add(0, row)
				if toggle {
					out.snippets[len(out.snippets)-1] = snippet
				}
			}
		}
	case entry.Agent == "You":
		for k := first; k <= last; k++ {
			next := v.Entries[k].Source
			entryRows[next.Seq] = len(out.lines)
			v.userItemContinued(&out, next, width, k != first)
		}
	case v.conversationTool(entry):
		var group []activityui.Block
		unconfirmed := false
		restored := true
		for k := first; k <= last; k++ {
			if v.visible(v.Entries[k].Source) {
				n := v.Entries[k].Source.native
				restored = restored && n != nil && !n.live
				unconfirmed = unconfirmed || n.unconfirmed() && (n.output == nil || !n.output.View().Exited || n.output.View().Exit == 0)
				for _, block := range v.shownBlocks(k) {
					block.Source = v.Entries[k].Source.Seq
					block.TailRows = v.tailRows()
					group = append(group, block)
				}
			}
		}
		// The operations form a tree under the reasoning above them. An edit
		// group's later rows and output notes continue their operation's branch.
		var parts [][]string
		var toggles []liveActivitySnippet // Aligned with every rendered row, including grouped edits.
		// Its connectors sit beneath the reasoning bullet. Keep invocation identities
		// even when adjacent edits share a heading or edit the same path.
		if v.expansion == 0 {
			group = activityui.MergeLiveActivityReads(group)
		}
		laid = activityui.AlignVerbs(activityui.GroupOperations(group))
		// A batch that has scrolled out of view keeps one row, which opens
		// its operations in the output dialog. Failed exits stay visible in the
		// summary; unconfirmed host items without an exit keep their rows.
		// Restored groups start folded instead of losing their old viewport state.
		if collapsed, ok := activityui.CollapseBatch(laid); ok && !unconfirmed && v.excerpt(restored || v.passed[entry.Seq]) {
			laid = []activityui.Block{collapsed}
		} else {
			batch = ok && !unconfirmed
		}
		for index, block := range laid {
			block = v.discloseBlock(block)
			snippet := liveActivitySnippet{run: entry.Seq, block: index}
			toggle := v.clickTarget(&block, snippet, width-2)
			if block.Kind == "op" && block.Code != "" && v.expansion != 2 {
				// The output dialog shows the whole source.
				block.SourceRows = conversationSourceRows
			}
			target := liveActivitySnippet{}
			if block.EditSource != "" {
				target = liveActivitySnippet{run: block.Source, block: editNavigationSnippet, path: activityui.EditPath(block)}
			} else if toggle {
				target = snippet
			}
			rows := v.paintBlock(len(toggles), 0, func() []string { return p.Block(v.outputContext(block), width-2) })
			if len(block.Questions) > 0 {
				entryRows[block.Source] = len(toggles)
			}
			if len(parts) > 0 && (block.Kind == "filter" || block.GroupHeader != "" && !block.GroupStart) {
				parts[len(parts)-1] = append(parts[len(parts)-1], rows...)
			} else {
				parts = append(parts, rows)
			}
			for range rows {
				toggles = append(toggles, target)
			}
		}
		for i, row := range activityui.Tree(parts) {
			out.add(0, row)
			out.snippets[len(out.snippets)-1] = toggles[i]
		}
	case entry.Agent == v.conversationSpeaker() && (entry.Kind == "text" || v.childrenOnly && entry.Kind == "final"):
		out.add(0, mainHeading(p, entry, width))
		gutter := mainGutter(p)
		if entry.native != nil && entry.native.question != 0 && v.mainReplyQuotes(first, entry.native.question) {
			for _, question := range v.Entries[:first] {
				if question.Source.Seq == entry.native.question {
					v.replyContext(&out, question.Source, gutter, width-2)
					out.add(0, gutter) // End the user excerpt before Main's own Markdown.
					break
				}
			}
		}
		out.hang(gutter, gutter, p.Markdown(entry.Text, width-2, false))
	default:
		v.agentItem(&out, entry, blocks, first, width, thread)
		if entry.Kind == "error" && len(blocks) > 0 && activityui.ErrorHasDetails(blocks[0]) {
			laid = blocks
			for row := range out.snippets {
				out.snippets[row] = liveActivitySnippet{run: entry.Seq, block: 0}
			}
		}
	}
	return liveActivityRun{lines: out.lines, blocks: laid, snippets: out.snippets, questions: out.questions, entryRows: entryRows, batch: batch}
}

// conversationHeading is one item heading: a glyph, a name, optional dim
// detail, and the time right-aligned when it fits.
func conversationHeading(glyph, name, detail string, entry activityPaneEntry, width int) string {
	return activityui.EventHeading(glyph, name, detail, entry.Observed, width)
}

// mainHeading and mainGutter mark Main's own replies the way agent traffic
// is marked, so every transcript item starts with who spoke and when.
func mainHeading(p *activityui.Painter, entry activityPaneEntry, width int) string {
	name := "\x1b[1m" + p.Theme.Accent() + "main" + terminalui.Reset
	if entry.Agent != "Main" {
		name = p.Agent(entry.Agent)
	}
	return conversationHeading(p.Theme.Accent()+"●"+terminalui.Reset, name, "", entry, width)
}

func mainGutter(p *activityui.Painter) string {
	return p.Theme.Accent() + "┃" + terminalui.Reset + " "
}

func (v *liveActivityView) userItemContinued(out *conversationLines, entry activityPaneEntry, width int, continued bool) {
	band := userBand(v.painter.Theme)
	var rows []string
	if entry.native != nil && len(entry.native.spans) > 0 {
		// Bound input stays literal; Markdown must not consume token text.
		rows, _ = activityui.LayoutSpans(entry.Text, entry.native.spans, width-2)
	} else {
		rows = v.paintBlock(len(out.lines), 0, func() []string { return v.painter.Markdown(entry.Text, width-2, false) })
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	stamp := terminalui.Dim + entry.Observed.Local().Format("15:04:05") + terminalui.Undim
	for k, row := range rows {
		lead := "  "
		if k == 0 && !continued {
			lead = liveActivityPrompt + "❯" + terminalui.Reset + " "
		}
		line := terminalui.Reset + ansi.Truncate(lead+row, width, "…") + terminalui.Reset
		if k == 0 && !continued && ansi.StringWidth(line)+2+ansi.StringWidth(stamp) <= width {
			line += strings.Repeat(" ", width-ansi.StringWidth(line)-ansi.StringWidth(stamp)) + stamp
		}
		if band != "" {
			line = strings.ReplaceAll(line, terminalui.Reset, terminalui.Reset+band)
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
	glyph, detail := terminalui.Dim+"●"+terminalui.Undim, ""
	var block activityui.Block
	if len(blocks) > 0 {
		block = blocks[0]
	}
	agent := trafficAgent(entry, block)
	if agent == "" {
		agent = entry.Agent
	}
	reply := block.From != "/root" && block.Kind != "start" && entry.Kind != "assignment"
	switch {
	case block.Kind == "start":
		glyph, detail = terminalui.Green+"▶"+terminalui.Reset, "started"
		if block.Label != "" {
			detail += " · " + ansi.Strip(p.Inline(block.Label))
		}
	case block.Kind == "message" && block.From == "/root":
		glyph = terminalui.Dim + "→" + terminalui.Undim
		if thread.joined {
			detail = "follow-up"
		}
	case block.Kind == "message":
		glyph = terminalui.Dim + "←" + terminalui.Undim
		if thread.joined {
			detail = "replied"
		}
	case block.Kind == "final":
		glyph, detail = terminalui.Green+"✓"+terminalui.Reset, "finished"
	case block.Kind == "error":
		glyph = terminalui.Red + "✗" + terminalui.Reset
		if thread.joined {
			detail = "failed"
		}
	}
	name := p.Agent(agent)
	if !v.conversation {
		agent = entry.Agent
		name = p.Agent(agent)
		if block.Kind == "message" {
			glyph, name = terminalui.Dim+"✉"+terminalui.Undim, p.Route(block)
		}
		if i := slices.IndexFunc(v.Agents, func(a activityPaneAgent) bool { return a.Name == entry.Agent }); i >= 0 {
			if role := liveActivityRole(v.Agents[i]); role != "" {
				detail += " · " + role
			}
		}
		detail = strings.TrimSpace(detail)
	}
	color := activityui.Gutter(agent, p.Theme)
	if !v.conversation && block.Kind == "message" {
		color = activityui.Gutter(block.From, p.Theme)
	}
	if thread.joined {
		out.add(0, threadHeading(color+"├─"+terminalui.Reset+glyph, detail, entry, v.Entries[thread.previous].Source, reply, width))
	} else {
		out.add(0, conversationHeading(glyph, name, detail, entry, width))
	}
	gutter := color + "│" + terminalui.Reset + " "
	tail, limit := color+"╰─"+terminalui.Reset, conversationLatestRows
	if thread.followed {
		tail, limit = gutter, conversationEarlierRows
	}
	body := width - 2
	switch {
	case !v.conversation && block.Kind == "final" && v.expansion == 0:
		block.Flash = v.flashQuestion == entry.Seq && v.now().Before(v.flashUntil)
		out.hang(gutter, gutter, v.paintBlock(len(out.lines), 5, func() []string { return p.Event(block, body) }))
	case block.Kind == "start" || block.Kind == "message":
		switch {
		case entry.activitySeq != 0 && reply:
			v.replyExcerpt(out, entry, block.Body, gutter, tail, body, limit)
		case v.excerpt(v.passed[entry.Seq]) && v.sentExcerpt(out, entry, block, gutter, tail, body, limit):
		case thread.followed:
			v.collapsedItem(out, entry, p.Markdown(block.Body, body, false), gutter, body)
		default:
			out.hang(gutter, gutter, p.Markdown(block.Body, body, false))
		}
	default:
		for _, block := range blocks {
			if block.Kind == "error" {
				block.Hovered = v.snippet == (liveActivitySnippet{run: entry.Seq, block: 0})
				out.hang(gutter, gutter, activityui.ErrorRows(block, body))
				continue
			}
			if block.Kind == "final" {
				// A plain answer links to the latest task it could answer.
				for _, question := range slices.Backward(v.Entries[:index]) {
					if (question.Source.Kind == "start" || question.Source.Kind == "assignment") && question.Source.assignment != nil && question.Source.assignment.to == agent {
						if !v.threadTask(thread, index, question.Source.Seq) {
							v.replyContext(out, question.Source, gutter, body)
						}
						break
					}
				}
				if entry.activitySeq != 0 {
					v.replyExcerpt(out, entry, block.Body, gutter, tail, body, limit)
				} else {
					out.hang(gutter, gutter, p.Markdown(block.Body, body, false))
				}
				continue
			}
			out.hang(gutter, gutter, v.paintBlock(len(out.lines), 0, func() []string { return p.Block(block, body) }))
		}
	}
}

// threadHeading continues a thread from the item above. A reply shows how
// long the agent took rather than a second clock time.
func threadHeading(lead, detail string, entry, previous activityPaneEntry, reply bool, width int) string {
	if reply && !entry.Observed.IsZero() && !previous.Observed.IsZero() {
		detail = strings.TrimPrefix(detail+" · "+activityui.FormatDuration(entry.Observed.Sub(previous.Observed)), " · ")
	}
	head := lead
	if detail != "" {
		head += " " + terminalui.Dim + detail + terminalui.Undim
	}
	head = ansi.Truncate(head, width, "…")
	if !reply {
		stamp := terminalui.Dim + entry.Observed.Local().Format("15:04:05") + terminalui.Undim
		if gap := width - ansi.StringWidth(head) - ansi.StringWidth(stamp); gap >= 2 {
			head += strings.Repeat(" ", gap) + stamp
		}
	}
	return head
}

// mainReplyQuotes reports whether Main's reply at index quotes its message:
// the first reply to it does, and a later one only when another message sits
// between them. Main's own tools, reasoning and progress are not messages.
func (v *liveActivityView) mainReplyQuotes(index int, question uint64) bool {
	for _, previous := range slices.Backward(v.Entries[:index]) {
		if previous.Source.Seq == question {
			return true
		}
		if !v.visible(previous.Source) || previous.Source.Agent == v.conversationSpeaker() && previous.Source.Kind != "text" && previous.Source.Kind != "final" {
			continue
		}
		return previous.Source.Agent != v.conversationSpeaker() || previous.Source.Kind != "text" || previous.Source.native == nil || previous.Source.native.question != question
	}
	return true
}

// replyContext gives every reply the same header and separately quoted prompt.
// Both the header and excerpt navigate to the retained original, when loaded.
func (v *liveActivityView) replyContext(out *conversationLines, question activityPaneEntry, gutter string, width int) {
	target := "your message"
	if question.Kind == "start" || question.Kind == "assignment" {
		target = "assignment"
	} else if question.Kind == "question_reply" {
		target = "your answer"
	}
	text := question.Text
	if question.assignment != nil {
		text = question.assignment.text
	}
	header := v.painter.Theme.Accent() + "↩ re: " + target + terminalui.Reset
	if question.Seq == 0 {
		header = terminalui.Dim + "↩ re: original message (not loaded)" + terminalui.Undim
	} else if !question.Observed.IsZero() {
		header += terminalui.Dim + " " + question.Observed.Local().Format("15:04:05") + terminalui.Undim
	}
	out.add(question.Seq, gutter+ansi.Truncate(header, width, "…"))
	rows, _ := liveActivityExcerpt(v.painter.Quote(textsanitize.Safe(text, false), width), width, 2)
	for _, row := range rows {
		out.add(question.Seq, gutter+row)
	}
}

func (v *liveActivityView) replyExcerpt(out *conversationLines, entry activityPaneEntry, text, gutter, tail string, width, limit int) {
	v.linkedExcerpt(out, entry, "reply", v.painter.Markdown(text, width, false), gutter, tail, width, limit)
}

// sentExcerpt shortens a sent message that has scrolled out of view the way a
// reply is shortened. It adds nothing and reports false when the excerpt would
// omit nothing, since the link would then only add a row.
func (v *liveActivityView) sentExcerpt(out *conversationLines, entry activityPaneEntry, block activityui.Block, gutter, tail string, width, limit int) bool {
	rows := v.painter.Markdown(block.Body, width, false)
	if _, hidden := liveActivityExcerpt(rows, width, limit); entry.activitySeq == 0 || hidden == 0 {
		return false
	}
	noun := "message"
	if block.Kind == "start" || entry.Kind == "assignment" {
		noun = "assignment"
	}
	v.linkedExcerpt(out, entry, noun, rows, gutter, tail, width, limit)
	return true
}

func (v *liveActivityView) linkedExcerpt(out *conversationLines, entry activityPaneEntry, noun string, rows []string, gutter, tail string, width, limit int) {
	if v.expansion != 0 {
		out.hang(gutter, gutter, rows)
		return
	}
	rows, hidden := liveActivityExcerpt(rows, width, limit)
	out.hang(gutter, gutter, rows)
	link := v.painter.Theme.Accent() + "↩ Open " + noun + terminalui.Reset
	if hidden > 0 {
		link += terminalui.Dim + " · " + terminalui.Undim + activityui.Elision{Hidden: hidden, Form: activityui.ElisionSuffix}.String()
	}
	out.add(entry.Seq, tail+ansi.Truncate(link, width, "…"))
}

// collapsedItem shortens an item its thread has moved past. Assignments have
// no Activity entry to open, so the dialog resolves their own source entry.
func (v *liveActivityView) collapsedItem(out *conversationLines, entry activityPaneEntry, rows []string, gutter string, width int) {
	if v.expansion != 0 {
		out.hang(gutter, gutter, rows)
		return
	}
	snippet := liveActivitySnippet{run: entry.Seq, block: 0}
	_, hidden := liveActivityExcerpt(rows, width, conversationEarlierRows)
	if hidden > 0 {
		hint := activityui.Elision{Hidden: hidden, Form: activityui.ElisionSuffix, Hovered: v.snippet == snippet}.String()
		room := width - ansi.StringWidth(hint) - 1
		rows, _ = liveActivityExcerpt(rows, room, conversationEarlierRows)
		last := &rows[len(rows)-1]
		*last += strings.Repeat(" ", max(1, width-ansi.StringWidth(*last)-ansi.StringWidth(hint))) + hint
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
			kept[limit-1] = ansi.Truncate(kept[limit-1], max(0, width-1), "") + terminalui.Dim + "…" + terminalui.Undim
		}
		hidden++
	}
	return kept, hidden
}
