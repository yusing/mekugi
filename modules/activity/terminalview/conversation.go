package terminalview

// Source: apps/mekugi/internal/router/live_activity_conversation.go:15:847@[29de53cc].

import (
	"cmp"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// PromptColor marks the user's text in the composer and transcript.
const PromptColor = "\x1b[38;2;196;167;231m"

// UserBand tints the user's prompts so turns are easy to find while scrolling.
// An undetected theme keeps the terminal background rather than guessing.
func UserBand(theme diffrender.Theme) string {
	switch theme {
	case diffrender.LightTheme:
		return "\x1b[48;2;240;238;246m"
	case diffrender.DarkTheme:
		return "\x1b[48;2;34;32;42m"
	}
	return ""
}

func (v *View) conversationSpeaker() string {
	if v.Conversation && v.ChildrenOnly && v.displayingChild() {
		return v.displayedCaller()
	}
	return "Main"
}

// conversationTool groups the displayed speaker's operations so adjacent reads merge.
func (v *View) conversationTool(entry activitypresentation.Entry) bool {
	return entry.Agent == v.conversationSpeaker() && slices.Contains([]string{"tool", "command", "hook", "attachments", "question"}, entry.Kind)
}

type conversationItem struct {
	first, last int
	agent       string // Thread agent, or "" when the item cannot join one.
	aside       bool   // Main reasoning, which neither starts nor ends a thread.
	lead        int    // Entry index of the Main item a tool group continues, or -1.
	attached    bool   // Tools branching from the reasoning directly above, with no gap.
}

type conversationPaint struct{ first, last, lead int }

// RenderConversation lays messages out as a transcript rather than an activity
// feed: prompts on a tinted band, Main's own messages without
// author headings, and agent traffic under one-line headings. Adjacent traffic
// with one agent forms a thread. Main's reasoning between a thread's items
// neither breaks it nor enters its rail: it follows the thread instead. Main's
// tools never sit headless below agent traffic: the reasoning or commentary
// they continue moves below the traffic, or, when it already heads earlier
// tools, a continuation row names it. It reuses Activity's block parsing,
// painter and viewport logic.
func (v *View) RenderConversation(width int) Feed {
	v.beginRuns()
	items := v.conversationItems[:0]
	// A question keeps its own rows without preventing operation groups on
	// either side from folding out of view.
	tools := func(entry activitypresentation.Entry) bool {
		return v.conversationTool(entry) && entry.Kind != "question" && len(entry.Images) == 0
	}
	for i := 0; i < len(v.entries); {
		if !v.Visible(v.entries[i]) {
			i++
			continue
		}
		last, j := i, i+1
		// Codex can report consecutive steers as separate user items even
		// though they form one uninterrupted input in the transcript.
		if first := v.entries[i]; (first.Agent == "You" || first.Kind == "user") && first.InputGroup != "" {
			for ; j < len(v.entries); j++ {
				next := v.entries[j]
				if !v.Visible(next) || (next.Agent != "You" && next.Kind != "user") || next.InputGroup != first.InputGroup {
					break
				}
				last = j
			}
		}
		if v.entries[i].Agent == v.conversationSpeaker() && v.entries[i].Kind == "reasoning" {
			for ; j < len(v.entries); j++ {
				next := v.entries[j]
				if !v.Visible(next) {
					continue
				}
				if next.Agent != v.conversationSpeaker() || next.Kind != "reasoning" {
					break
				}
				last = j
			}
		}
		for _, same := range []func(activitypresentation.Entry) bool{tools} {
			if !same(v.entries[i]) {
				continue
			}
			for ; j < len(v.entries) && (!v.Visible(v.entries[j]) || same(v.entries[j])); j++ {
				if v.Visible(v.entries[j]) {
					last = j
				}
			}
			break
		}
		entry := v.entries[i]
		items = append(items, conversationItem{first: i, last: last, agent: v.threadAgent(i), aside: entry.Agent == v.conversationSpeaker() && entry.Kind == "reasoning", lead: -1})
		i = j
	}
	// Warm runs already know the row count. Reserve the parallel row slices
	// together instead of repeatedly copying the whole transcript as they grow.
	rowCapacity := len(items) // Allow separators and a changing tail.
	for key, run := range v.Runs {
		if key.Expansion == 0 || key.Expansion == v.Expansion {
			rowCapacity += len(run.Lines)
		}
	}
	feed := Feed{
		Lines:     make([]string, 0, rowCapacity),
		Heads:     make([]int, 0, rowCapacity),
		Snippets:  make([]Snippet, 0, rowCapacity),
		Questions: make([]uint64, 0, rowCapacity),
	}
	continues := func(a, b int) bool {
		return a >= 0 && b < len(items) && items[a].agent != "" && items[a].agent == items[b].agent
	}
	// Hold asides the thread continues past until the thread ends.
	ordered := v.conversationOrdered[:0]
	held := v.conversationHeld[:0]
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
	clear(items)
	v.conversationItems = items[:0]
	items = append(ordered, held...)
	clear(held[:cap(held)])
	v.conversationHeld = held[:0]
	traffic := func(it conversationItem) bool {
		entry := v.entries[it.first]
		return it.agent != "" || entry.Agent != v.conversationSpeaker() && entry.Agent != "You"
	}
	lead, headed := -1, false // Latest lead's position, and whether tools follow it.
	for k := 0; k < len(items); k++ {
		switch entry := v.entries[items[k].first]; {
		case (entry.Agent == "You" || entry.Kind == "user"):
			lead = -1
		case v.conversationLead(items[k].first):
			lead, headed = k, false
		case v.conversationTool(entry):
			if lead >= 0 && traffic(items[k-1]) {
				if !headed && !slices.ContainsFunc(items[lead+1:k], func(it conversationItem) bool { return !traffic(it) }) {
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
		items[k].attached = items[k].lead < 0 && v.conversationTool(v.entries[items[k].first]) &&
			previous.aside && v.entries[previous.first].Agent == v.conversationSpeaker() && v.conversationLead(previous.first)
	}
	start := 0 // Thread's first item.
	for k, it := range items {
		var thread ConversationThread
		if continues(k-1, k) {
			thread = ConversationThread{Joined: true, First: items[start].first, Previous: items[k-1].first}
		} else {
			start = k
		}
		thread.Followed = continues(k, k+1)
		key := RunKey{First: v.entries[it.first].Seq, Last: v.entries[it.last].Seq, Revision: v.runRevision(it.first, it.last+1), Width: width, Theme: v.Painter.Theme, Hover: -1, Main: true, Thread: thread, Excerpt: v.excerpt(v.Passed[v.entries[it.first].Seq]), Tail: v.tailRows(), Expansion: v.Expansion}
		if v.entries[it.first].Agent == "You" || v.entries[it.first].Kind == "user" {
			key.Expansion, key.Excerpt = 0, false
		}
		if it.lead >= 0 {
			key.Lead = v.entries[it.lead].Seq
		}
		if v.Snippet.Run == key.First {
			key.Hover = v.Snippet.Block
		}
		run, ok := v.Runs[key]
		if ok && !run.Colored && !v.Painter.LayoutOnly {
			ok = false
		}
		for k := it.first; k <= it.last; k++ {
			if v.entries[k].Running {
				ok = false
				break
			}
		}
		paint := conversationPaint{it.first, it.last, it.lead}
		if !ok {
			run = v.renderConversationRun(paint, key)
			run.Colored = !v.Painter.LayoutOnly
		}
		run.generation = v.runGeneration
		v.Runs[key] = run
		if len(feed.Lines) > 0 && !thread.Joined && !it.attached {
			feed.separator()
		}
		head := len(feed.Lines)
		if !run.Colored {
			if feed.paints == nil {
				feed.paints = make([]liveActivityPaint, 0, len(items)-k)
			}
			feed.paints = append(feed.paints, liveActivityPaint{start: head, key: key, conversation: paint})
		}
		// A sent message or batch shrinking above a scrolled viewport keeps its rows still.
		if full := key; !ok && key.Excerpt && !v.Following && head < v.Offset {
			full.Excerpt = false
			if shown, ok := v.Runs[full]; ok {
				v.Offset -= len(shown.Lines) - len(run.Lines)
			}
		}
		for seq, row := range run.EntryRows {
			v.QuestionRows[seq] = head + row
		}
		if entry := v.entries[it.first]; entry.Kind == "start" || entry.Kind == "assignment" {
			v.QuestionRows[entry.Seq] = head
		}
		owner := it.first
		for row := range run.Lines {
			start := head
			if v.entries[it.first].Agent == "You" || v.entries[it.first].Kind == "user" {
				for owner < it.last && run.EntryRows[v.entries[owner+1].Seq] <= row {
					owner++
				}
				start += run.EntryRows[v.entries[owner].Seq]
			}
			feed.Heads = append(feed.Heads, start)
		}
		feed.appendRows(run)
		v.appendImages(&feed, it.first, it.last, head, width)
		feed.Spans = append(feed.Spans, Span{key.First, head, len(feed.Lines)})
		if v.sentMessage(it.first) || !key.Excerpt && run.Batch {
			feed.Passing = append(feed.Passing, liveActivityPassing{key.First, len(feed.Lines)})
		}
	}
	for _, entry := range v.entries {
		if entry.Kind == "question_reply" {
			if row, ok := v.QuestionRows[entry.Question]; ok {
				v.QuestionRows[entry.Seq] = row
			}
		}
	}
	v.retainRuns()
	clear(items)
	v.conversationOrdered = items[:0]
	return feed
}

func (v *View) renderConversationRun(it conversationPaint, key RunKey) Run {
	window := v.syntaxWindow
	if window != nil && it.lead >= 0 {
		v.syntaxWindow = &liveActivitySyntaxWindow{window.first - 1, window.last - 1}
	}
	run := v.ConversationItem(it.first, it.last, key.Width, key.Thread)
	v.syntaxWindow = window
	if it.lead >= 0 {
		continuation := v.paintBlock(0, 1, func() []string { return []string{v.Continuation(it.lead, key.Width)} })
		run.Lines = append(continuation, run.Lines...)
		run.Snippets = append([]Snippet{{}}, run.Snippets...)
		run.Questions = append([]uint64{0}, run.Questions...)
		for seq, row := range run.EntryRows {
			run.EntryRows[seq] = row + 1
		}
	}
	return run
}

// sentMessage reports a spawn, follow-up or non-incoming message with an Activity
// entry, which becomes an excerpt once it has scrolled out of view.
func (v *View) sentMessage(index int) bool {
	entry, blocks := v.entries[index], v.entries[index].Values
	if entry.Activity == 0 || len(blocks) == 0 {
		return false
	}
	return blocks[0].Kind == "start" || entry.Kind == "assignment" || blocks[0].Kind == "message" && !v.conversationLocal(blocks[0].To)
}

// conversationLead reports Main reasoning or commentary, which heads the
// tools that follow it.
func (v *View) conversationLead(index int) bool {
	entry, blocks := v.entries[index], v.entries[index].Values
	switch {
	case entry.Agent != v.conversationSpeaker():
		return false
	case entry.Kind == "reasoning":
		return len(blocks) > 0 && activitypresentation.ReasoningSummaryBody(blocks[0].Body) != ""
	}
	return entry.Kind == "text" && strings.TrimSpace(entry.Text) != ""
}

// Continuation is one row naming the lead a tool group resumes after agent
// traffic, so the tools cannot read as that traffic's.
func (v *View) Continuation(index, width int) string {
	entry, p := v.entries[index], &v.Painter
	if entry.Kind == "text" {
		return conversationHeading(p.Theme.Accent()+"●"+terminalui.Reset, "\x1b[1m"+p.Theme.Accent()+"main"+terminalui.Reset, "continued", entry, width)
	}
	suffix := terminalui.Dim + " · continued" + terminalui.Undim
	row := p.Block(v.blocks(index)[0], width)[0]
	return ansi.Truncate(row, max(0, width-ansi.StringWidth(suffix)), "…") + terminalui.Reset + suffix
}

// ConversationThread places an item in a thread: adjacent agent traffic with
// one agent, which shares a gutter instead of repeating headings and quotes.
type ConversationThread struct {
	Joined          bool // Continues the thread item above.
	First, Previous int  // Entry indexes of the thread's first item and the item above, when joined.
	Followed        bool // A later item continues the thread.
}

// excerpt budgets: an item its thread has moved past keeps a short excerpt,
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
func (v *View) threadAgent(index int) string {
	entry := v.entries[index]
	if (entry.Agent == "You" || entry.Kind == "user") || entry.Agent == v.conversationSpeaker() && entry.Kind != "start" && entry.Kind != "assignment" && entry.Kind != "reply" || len(v.entries[index].Values) == 0 {
		return ""
	}
	return v.trafficAgent(entry, v.blocks(index)[0])
}

// conversationCaller is the canonical endpoint of the displayed transcript.
func (v *View) conversationCaller() string {
	if speaker := v.conversationSpeaker(); speaker != "Main" {
		return speaker
	}
	return cmp.Or(v.Display.MainCaller, "/root")
}

// Main is local to the runtime's own transcript. It is not the selected child
// when viewing that child's entries in the shared Activity feed.
func (v *View) conversationLocal(endpoint string) bool {
	return endpoint == v.conversationCaller() || endpoint == "Main" && v.conversationSpeaker() == "Main"
}

// trafficAgent names the started agent or the other peer of a local message.
// Traffic between other agents must retain its own route, outside a local thread.
func (v *View) trafficAgent(entry activitypresentation.Entry, block Block) string {
	agent := ""
	switch {
	case block.Kind == "start":
		if block.From != "" && block.To != "" && !v.conversationLocal(block.From) && !v.conversationLocal(block.To) {
			return ""
		}
		agent = block.To
	case block.Kind == "message":
		switch {
		case v.conversationLocal(block.From):
			agent = block.To
		case v.conversationLocal(block.To):
			agent = block.From
		default:
			return ""
		}
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
func (v *View) threadTask(thread ConversationThread, index int, seq uint64) bool {
	if !thread.Joined || seq == 0 {
		return false
	}
	for _, entry := range slices.Backward(v.entries[thread.First:index]) {
		if entry.Kind == "start" || entry.Kind == "assignment" {
			return entry.Seq == seq
		}
	}
	return false
}

type conversationLines struct {
	lines     []string
	questions []uint64
	snippets  []Snippet
}

func (c *conversationLines) add(target uint64, lines ...string) {
	for _, line := range lines {
		c.lines = append(c.lines, line)
		c.questions = append(c.questions, target)
		c.snippets = append(c.snippets, Snippet{})
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

func (v *View) ConversationItem(first, last, width int, thread ConversationThread) Run {
	entry := v.entries[first]
	v.Painter.CopyScope = entry.Seq
	blocks := v.blocks(first)
	var out conversationLines
	var laid []Block // Tool blocks, indexed by the snippets naming them.
	batch := false
	entryRows := make(map[uint64]int)
	p := &v.Painter
	switch {
	case entry.Agent == v.conversationSpeaker() && (entry.Kind == "reasoning" || entry.Kind == "progress" || entry.Kind == "compaction"):
		var summaries []Block
		for k := first; k <= last; k++ {
			if v.Visible(v.entries[k]) {
				for _, block := range v.blocks(k) {
					block.Source = v.entries[k].Seq
					summaries = append(summaries, block)
				}
			}
		}
		if v.Expansion == 0 {
			summaries = CompactReasoning(summaries)
		}
		laid = summaries
		for index, block := range laid {
			block = v.discloseBlock(block)
			// Thinking and reset disclosures share the existing output dialog.
			snippet := Snippet{Run: entry.Seq, Block: index}
			toggle := v.ClickTarget(&block, snippet, width)
			for _, row := range v.paintBlock(len(out.lines), 0, func() []string { return p.Block(v.outputContext(block), width) }) {
				out.add(0, row)
				if toggle {
					out.snippets[len(out.snippets)-1] = snippet
				}
			}
		}
	case (entry.Agent == "You" || entry.Kind == "user"):
		for k := first; k <= last; k++ {
			next := v.entries[k]
			entryRows[next.Seq] = len(out.lines)
			v.userItemContinued(&out, next, width, k != first)
		}
	case v.conversationTool(entry):
		var group []Block
		unconfirmed := false
		restored := true
		for k := first; k <= last; k++ {
			if v.Visible(v.entries[k]) {
				n := v.entries[k]
				restored = restored && n.Restored
				unconfirmed = unconfirmed || n.Unconfirmed && !n.OutputFailed
				for _, block := range v.shownBlocks(k) {
					block.Source = v.entries[k].Seq
					block.TailRows = v.tailRows()
					group = append(group, block)
				}
			}
		}
		// The operations form a tree under the reasoning above them. An operation
		// group's later rows and output notes continue their operation's branch.
		var parts [][]string
		var toggles []Snippet // Aligned with every rendered row, including grouped operations.
		// Its connectors sit beneath the reasoning bullet. Keep invocation identities
		// even when adjacent edits share a heading or edit the same path.
		if v.Expansion == 0 {
			group = MergeTargets(group)
		}
		laid = GroupOperations(group)
		// A batch that has scrolled out of view keeps one row, which opens
		// its operations in the output dialog. Failed exits stay visible in the
		// summary; unconfirmed host items without an exit keep their rows.
		// Restored groups start folded instead of losing their old viewport state.
		if collapsed, ok := CollapseBatch(laid); ok && !unconfirmed && v.excerpt(restored || v.Passed[entry.Seq]) {
			laid = []Block{collapsed}
		} else {
			batch = ok && !unconfirmed
			if v.Expansion != 2 {
				laid = collapseCapturedEdits(laid)
			}
		}
		laid = AlignVerbs(laid)
		for index, block := range laid {
			block = v.discloseBlock(block)
			snippet := Snippet{Run: entry.Seq, Block: index}
			toggle := v.ClickTarget(&block, snippet, width-2)
			if block.Kind == "op" && block.Code != "" && v.Expansion != 2 {
				// The output dialog shows the whole source.
				block.SourceRows = conversationSourceRows
			}
			target := Snippet{}
			if block.EditOperation() {
				target = Snippet{Run: block.Source, Block: EditNavigationSnippet, Path: block.EditPath()}
			} else if toggle {
				target = snippet
			}
			rows := v.paintBlock(len(toggles), 0, func() []string { return p.Block(v.outputContext(block), width-2) })
			if len(block.Questions) > 0 {
				entryRows[block.Source] = len(toggles)
			}
			if len(parts) > 0 && (block.Effect || block.Kind == "filter" || block.GroupHeader != "" && !block.GroupStart) {
				parts[len(parts)-1] = append(parts[len(parts)-1], rows...)
			} else {
				parts = append(parts, rows)
			}
			for range rows {
				toggles = append(toggles, target)
			}
		}
		for i, row := range Tree(parts) {
			out.add(0, row)
			out.snippets[len(out.snippets)-1] = toggles[i]
		}
	case entry.Agent == v.conversationSpeaker() && (entry.Kind == "text" || v.ChildrenOnly && entry.Kind == "final"):
		out.add(0, mainHeading(p, entry, width))
		gutter := mainGutter(p)
		if entry.Question != 0 && v.mainReplyQuotes(first, entry.Question) {
			for _, question := range v.entries[:first] {
				if question.Seq == entry.Question {
					v.replyContext(&out, question, gutter, width-2)
					out.add(0, gutter) // End the user excerpt before Main's own Markdown.
					break
				}
			}
		}
		out.hang(gutter, gutter, p.Markdown(entry.Text, width-2, false))
	default:
		v.agentItem(&out, entry, blocks, first, width, thread)
		if len(blocks) > 0 && (blocks[0].DetailCode != "" || entry.Kind == "error" && ErrorHasDetails(blocks[0])) {
			laid = blocks
			for row := range out.snippets {
				out.snippets[row] = Snippet{Run: entry.Seq, Block: 0}
			}
		}
	}
	return Run{Lines: out.lines, Blocks: laid, Snippets: out.snippets, Questions: out.questions, EntryRows: entryRows, Batch: batch}
}

// conversationHeading is one item heading: a glyph, a name, optional dim
// detail, and the time right-aligned when it fits.
func conversationHeading(glyph, name, detail string, entry activitypresentation.Entry, width int) string {
	return EventHeading(glyph, name, detail, entry.Observed, width)
}

// mainHeading and mainGutter mark Main's own replies the way agent traffic
// is marked, so every transcript item starts with who spoke and when.
func mainHeading(p *Painter, entry activitypresentation.Entry, width int) string {
	name := "\x1b[1m" + p.Theme.Accent() + "main" + terminalui.Reset
	if entry.Agent != "Main" {
		name = p.Agent(entry.Agent)
	}
	return conversationHeading(p.Theme.Accent()+"●"+terminalui.Reset, name, "", entry, width)
}

func mainGutter(p *Painter) string {
	return p.Theme.Accent() + "┃" + terminalui.Reset + " "
}

func (v *View) userItemContinued(out *conversationLines, entry activitypresentation.Entry, width int, continued bool) {
	band := UserBand(v.Painter.Theme)
	var rows []string
	if len(entry.Spans) > 0 {
		// Bound input stays literal; Markdown must not consume token text.
		rows, _ = LayoutSpans(entry.Text, entry.Spans, width-2)
	} else {
		rows = v.paintBlock(len(out.lines), 0, func() []string { return v.Painter.Markdown(entry.Text, width-2, false) })
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	stamp := ""
	if entry.Kind != "user" || !entry.Observed.IsZero() {
		stamp = terminalui.Dim + entry.Observed.Local().Format("15:04:05") + terminalui.Undim
	}
	for k, row := range rows {
		lead := "  "
		if k == 0 && !continued {
			lead = PromptColor + "❯" + terminalui.Reset + " "
		}
		line := terminalui.Reset + ansi.Truncate(lead+row, width, "…") + terminalui.Reset
		if k == 0 && !continued && stamp != "" && ansi.StringWidth(line)+2+ansi.StringWidth(stamp) <= width {
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
func (v *View) agentItem(out *conversationLines, entry activitypresentation.Entry, blocks []Block, index, width int, thread ConversationThread) {
	p := &v.Painter
	glyph, detail := terminalui.Dim+"●"+terminalui.Undim, ""
	var block Block
	if len(blocks) > 0 {
		block = blocks[0]
	}
	agent := v.trafficAgent(entry, block)
	external := agent == "" && block.From != "" && block.To != "" && (block.Kind == "message" || block.Kind == "start")
	if agent == "" {
		agent = entry.Agent
	}
	reply := block.Kind != "start" && entry.Kind != "assignment" && (block.Kind != "message" || v.conversationLocal(block.To))
	switch {
	case block.Kind == "start":
		glyph, detail = terminalui.Green+"▶"+terminalui.Reset, "started"
		if block.Label != "" {
			detail += " · " + ansi.Strip(p.Inline(block.Label))
		}
	case block.Kind == "message" && v.conversationLocal(block.From):
		glyph = terminalui.Dim + "→" + terminalui.Undim
		if thread.Joined {
			detail = "follow-up"
		}
	case block.Kind == "message":
		glyph = terminalui.Dim + "←" + terminalui.Undim
		if thread.Joined {
			detail = "replied"
		}
	case block.Kind == "final":
		glyph, detail = terminalui.Green+"✓"+terminalui.Reset, "finished"
		if entry.Outcome == "interrupted" {
			glyph, detail = terminalui.Dim+"■"+terminalui.Undim, "interrupted"
		}
		if entry.Outcome == "failed" {
			glyph, detail = terminalui.Red+"✗"+terminalui.Reset, "failed"
		}
	case block.Kind == "error":
		glyph = terminalui.Red + "✗" + terminalui.Reset
		if thread.Joined {
			detail = "failed"
		}
	}
	name := p.Agent(agent)
	if external {
		name = p.Route(block)
		if block.Kind == "message" {
			glyph = terminalui.Dim + "✉" + terminalui.Undim
		}
	}
	if !v.Conversation {
		agent = entry.Agent
		name = p.Agent(agent)
		if block.Kind == "message" {
			glyph, name = terminalui.Dim+"✉"+terminalui.Undim, p.Route(block)
		}
		if i := slices.IndexFunc(v.roster, func(row activitypresentation.AgentRow) bool { return row.Agent.Name == entry.Agent }); i >= 0 {
			if role := Role(v.roster[i].Agent); role != "" {
				detail += " · " + role
			}
		}
		detail = strings.TrimSpace(detail)
	}
	color := Gutter(agent, p.Theme)
	if external || !v.Conversation && block.Kind == "message" {
		color = Gutter(block.From, p.Theme)
	}
	if thread.Joined {
		out.add(0, threadHeading(color+"├─"+terminalui.Reset+glyph, detail, entry, v.entries[thread.Previous], reply, width))
	} else {
		out.add(0, conversationHeading(glyph, name, detail, entry, width))
	}
	gutter := color + "│" + terminalui.Reset + " "
	tail, limit := color+"╰─"+terminalui.Reset, conversationLatestRows
	if thread.Followed {
		tail, limit = gutter, conversationEarlierRows
	}
	body := width - 2
	switch {
	case !v.Conversation && block.Kind == "final" && v.Expansion == 0:
		block.Flash = v.FlashQuestion == entry.Seq && v.Now().Before(v.FlashUntil)
		out.hang(gutter, gutter, v.paintBlock(len(out.lines), 5, func() []string { return p.Event(block, body) }))
	case block.Kind == "start" || block.Kind == "message":
		switch {
		case entry.Activity != 0 && external && block.Kind == "message" && entry.Kind != "assignment":
			v.linkedExcerpt(out, entry, "message", p.Markdown(block.Body, body, false), gutter, tail, body, limit)
		case entry.Activity != 0 && reply:
			v.childReply(out, entry, block, gutter, tail, body, limit)
		case v.excerpt(v.Passed[entry.Seq]) && v.sentExcerpt(out, entry, block, gutter, tail, body, limit):
		case thread.Followed:
			v.collapsedItem(out, entry, p.Markdown(block.Body, body, false), gutter, body)
		default:
			out.hang(gutter, gutter, p.Markdown(block.Body, body, false))
		}
	default:
		for _, block := range blocks {
			if block.Kind == "error" {
				block.Hovered = v.Snippet == (Snippet{Run: entry.Seq, Block: 0})
				out.hang(gutter, gutter, ErrorRows(block, body))
				continue
			}
			if block.Kind == "final" {
				// A plain answer links to the latest task it could answer.
				for _, question := range slices.Backward(v.entries[:index]) {
					if (question.Kind == "start" || question.Kind == "assignment") && question.AssignmentTo == agent {
						if !v.threadTask(thread, index, question.Seq) {
							v.replyContext(out, question, gutter, body)
						}
						break
					}
				}
				if entry.Activity != 0 {
					v.childReply(out, entry, block, gutter, tail, body, limit)
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
func threadHeading(lead, detail string, entry, previous activitypresentation.Entry, reply bool, width int) string {
	if reply && !entry.Observed.IsZero() && !previous.Observed.IsZero() {
		detail = strings.TrimPrefix(detail+" · "+FormatDuration(entry.Observed.Sub(previous.Observed)), " · ")
	}
	head := lead
	if detail != "" {
		head += " " + terminalui.Dim + detail + terminalui.Undim
	}
	head = ansi.Truncate(head, width, "…")
	if !reply {
		stamp := ""
		if entry.Kind != "user" || !entry.Observed.IsZero() {
			stamp = terminalui.Dim + entry.Observed.Local().Format("15:04:05") + terminalui.Undim
		}
		if gap := width - ansi.StringWidth(head) - ansi.StringWidth(stamp); gap >= 2 {
			head += strings.Repeat(" ", gap) + stamp
		}
	}
	return head
}

// mainReplyQuotes reports whether Main's reply at index quotes its message:
// the first reply to it does, and a later one only when another message sits
// between them. Main's own tools, reasoning and progress are not messages.
func (v *View) mainReplyQuotes(index int, question uint64) bool {
	for _, previous := range slices.Backward(v.entries[:index]) {
		if previous.Seq == question {
			return true
		}
		if !v.Visible(previous) || previous.Agent == v.conversationSpeaker() && previous.Kind != "text" && previous.Kind != "final" {
			continue
		}
		return previous.Agent != v.conversationSpeaker() || previous.Kind != "text" || previous.Question != question
	}
	return true
}

// replyContext gives every reply the same header and separately quoted prompt.
// Both the header and excerpt navigate to the retained original, when loaded.
func (v *View) replyContext(out *conversationLines, question activitypresentation.Entry, gutter string, width int) {
	target := "your message"
	if question.Kind == "start" || question.Kind == "assignment" {
		target = "assignment"
	} else if question.Kind == "question_reply" {
		target = "your answer"
	}
	text := question.Text
	if question.Assignment {
		text = question.AssignmentText
	}
	header := v.Painter.Theme.Accent() + "↩ re: " + target + terminalui.Reset
	if question.Seq == 0 {
		header = terminalui.Dim + "↩ re: original message (not loaded)" + terminalui.Undim
	} else if !question.Observed.IsZero() {
		header += terminalui.Dim + " " + question.Observed.Local().Format("15:04:05") + terminalui.Undim
	}
	out.add(question.Seq, gutter+ansi.Truncate(header, width, "…"))
	rows, _ := excerptRows(v.Painter.Quote(textsanitize.Safe(text, false), width), width, 2)
	for _, row := range rows {
		out.add(question.Seq, gutter+row)
	}
}

func (v *View) childReply(out *conversationLines, entry activitypresentation.Entry, block Block, gutter, tail string, width, limit int) {
	if v.ChildReplySummary && block.ReplySummary != "" {
		out.hang(gutter, gutter, v.Painter.Markdown(block.ReplySummary, width, false))
		if v.Expansion == 0 {
			out.add(entry.Seq, tail+ansi.Truncate(v.Painter.Theme.Accent()+"↩ Open reply"+terminalui.Reset, width, "…"))
		} else {
			out.hang(gutter, gutter, v.Painter.Markdown(block.ReplyDetails, width, false))
		}
		return
	}
	v.replyExcerpt(out, entry, block.Body, gutter, tail, width, limit)
}

func (v *View) replyExcerpt(out *conversationLines, entry activitypresentation.Entry, text, gutter, tail string, width, limit int) {
	v.linkedExcerpt(out, entry, "reply", v.Painter.Markdown(text, width, false), gutter, tail, width, limit)
}

// sentExcerpt shortens a sent message that has scrolled out of view the way a
// reply is shortened. It adds nothing and reports false when the excerpt would
// omit nothing, since the link would then only add a row.
func (v *View) sentExcerpt(out *conversationLines, entry activitypresentation.Entry, block Block, gutter, tail string, width, limit int) bool {
	rows := v.Painter.Markdown(block.Body, width, false)
	if _, hidden := excerptRows(rows, width, limit); entry.Activity == 0 || hidden == 0 {
		return false
	}
	noun := "message"
	if block.Kind == "start" || entry.Kind == "assignment" {
		noun = "assignment"
	}
	v.linkedExcerpt(out, entry, noun, rows, gutter, tail, width, limit)
	return true
}

func (v *View) linkedExcerpt(out *conversationLines, entry activitypresentation.Entry, noun string, rows []string, gutter, tail string, width, limit int) {
	if v.Expansion != 0 {
		out.hang(gutter, gutter, rows)
		return
	}
	rows, hidden := excerptRows(rows, width, limit)
	out.hang(gutter, gutter, rows)
	if hidden == 0 {
		return
	}
	link := v.Painter.Theme.Accent() + "↩ Open " + noun + terminalui.Reset
	link += terminalui.Dim + " · " + terminalui.Undim + Elision{Hidden: hidden, Form: ElisionSuffix}.String()
	out.add(entry.Seq, tail+ansi.Truncate(link, width, "…"))
}

// collapsedItem shortens an item its thread has moved past. Assignments have
// no Activity entry to open, so the dialog resolves their own source entry.
func (v *View) collapsedItem(out *conversationLines, entry activitypresentation.Entry, rows []string, gutter string, width int) {
	if v.Expansion != 0 {
		out.hang(gutter, gutter, rows)
		return
	}
	snippet := Snippet{Run: entry.Seq, Block: 0}
	_, hidden := excerptRows(rows, width, conversationEarlierRows)
	if hidden > 0 {
		hint := Elision{Hidden: hidden, Form: ElisionSuffix, Hovered: v.Snippet == snippet}.String()
		room := width - ansi.StringWidth(hint) - 1
		rows, _ = excerptRows(rows, room, conversationEarlierRows)
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

// excerptRows keeps the first limit rows with content and counts the
// content rows left out. Paragraph gaps and empty quote rows are skipped so a
// truncated excerpt always ends its last visible text row with the ellipsis
// rather than leaving it on a row alone.
func excerptRows(rows []string, width, limit int) ([]string, int) {
	var kept []string
	hidden := 0
	for _, row := range rows {
		if BlankRow(row) {
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
