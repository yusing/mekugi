package router

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
)

const (
	liveActivityFeedLimit = 2000
	// Panes at least this wide place agent cards beside the feed.
	liveActivitySideColumns = 100
	// Operations one live invocation reports together, such as a shell
	// call's Skill, Read and Search, appear this far apart.
	liveActivityPaceStep = 80 * time.Millisecond
)

// Main and Activity each own one of these views, sharing the renderer. Entries carry
// sequence numbers, so a reconnect snapshot merges without duplicating retained rows.
type liveActivityView struct {
	clock          func() time.Time // Shared with the owning replay UI, nil for live time.
	agents         []activityPaneAgent
	entries        []liveActivityRecord
	revision       uint64 // Monotonic presentation revision shared by records.
	lastSeq        uint64
	selected       string
	hovered        string
	only           bool
	hits           []liveActivityHit
	following      bool
	offset         int
	rosterOffset   int
	rosterManual   bool
	rosterSelected string
	rosterEnd      int
	skillsHover    string // Agent whose loaded-skill count is under the pointer.
	skillsRequest  string // Agent whose active skills a roster click asked to open.
	skills         *liveActivitySkills
	retiredSkills  map[string]activeSkillSet // Loads in trimmed entries, still in their agent's context.
	skillHistory   map[string]bool           // Paginated owners: false until their current context is known.
	unseen         int
	status         string
	historyHint    string // Intentionally unloaded child history, separate from missing evidence.
	historyOrder   bool   // Stable IDs need not follow presentation order after older-page insertion.
	roleColors     map[string]string
	livePreviews   map[string][]string // Temporary caller-local transcript replacements.
	feedOnly       bool
	conversation   bool                        // Main uses the same feed/state with full, unclipped messages.
	childrenOnly   bool                        // Native Main already owns root activity; keep it out of the auxiliary feed.
	bare           bool                        // The shell's pane title replaces the heading and footer rows.
	focused        bool                        // Native Activity shows its key hints only while it has keyboard focus.
	lineCounts     map[string]livediff.Counts  // Captured edit lines by caller key, for the native roster.
	netCounts      *livediff.Counts            // Composed project outcome, independent of roster rows.
	rosterPace     map[string]rosterMetricPace // Roster metrics easing toward their latest values, by agent.
	rosterLines    map[string]livediff.Counts  // Line counts as last shown by the roster.
	rosterEasing   bool                        // The last roster frame showed metrics still easing.
	mainView       *liveActivityView           // Roster reads Main's state without duplicating its feed entries.
	painter        activityui.Painter
	osc            livediff.OSC
	runs           map[liveActivityRunKey]liveActivityRun
	syntaxWindow   *liveActivitySyntaxWindow    // Invocation-local rows being decorated within a run.
	paced          map[uint64]liveActivityPace  // Live invocations still revealing their operations, by entry.
	events         map[string]liveActivityEvent // Each agent's latest standalone event, which settles its output.
	pacedSeq       uint64                       // Entries up to this sequence have been considered for pacing.

	expansion       uint8 // 0 default, 1 expanded events, 2 expanded all.
	feedSpans       []liveActivitySpan
	expansionAnchor *liveActivitySpan // Top item and its local row before a toggle.

	// opening names the snippet requested in the shared dialog.
	snippet liveActivitySnippet // Hovered content target.
	opening liveActivitySnippet
	// passed holds journal groups, Main's sent messages and operation batches that have
	// scrolled above the viewport. Messages then show as excerpts linking to
	// Activity, and batches as one row opening their operations.
	// Journal groups retain compact previews opening their full details.
	passed map[uint64]bool

	// Geometry of the last frame, used by scrolling keys and the pointer.
	// feedSnippets holds each feed row's snippet, from screen row feedTop
	// between columns feedLeft and feedRight.
	feedLines, feedRows int
	feedSnippets        []liveActivitySnippet
	feedQuestions       []uint64
	copyRows            [][]activityui.CopySpan
	questionRows        map[uint64]int
	pendingTarget       uint64 // Cross-pane jump resolved after the destination layout is rendered.
	// questionHover is the pointed viewport row plus one; zero points at none.
	questionHover                        int
	editHover                            int // Pointed viewport row plus one, not the whole capture.
	flashQuestion                        uint64
	flashUntil                           time.Time
	feedTop, feedLeft, feedRight         int
	rosterTop, rosterBottom, rosterRight int
	width, height                        int
}

// liveActivitySnippet names a clippable block in the shared feed: the
// sequence of its run's first entry and its index in the run. Sequences start
// at one, so the zero value names no snippet.
// editNavigationSnippet routes a compact edit row to its captured change.
const editNavigationSnippet = -1

type liveActivitySnippet struct {
	run   uint64
	block int
	path  string // Exact edit-row target, before display shortening.
}

type liveActivityRun struct {
	colored   bool   // Syntax decoration has been materialized.
	painted   []bool // Decorated rows in a partially visible run.
	lines     []string
	blocks    []activityui.Block    // Laid-out blocks, indexed by the snippets naming them.
	snippets  []liveActivitySnippet // Aligned with lines.
	questions []uint64              // Clickable question targets, aligned with lines.
	entryRows map[uint64]int        // Exact Activity entry starts within a grouped run.
	batch     bool                  // Main operations that fold into one row once out of view.
	running   []int                 // First row of each running command block.
}

// liveActivityEvent is an agent's latest standalone entry and when it arrived.
type liveActivityEvent struct {
	seq uint64
	at  time.Time
}

// liveActivityPace reveals a live invocation's operations one at a time.
type liveActivityPace struct {
	shown int
	next  time.Time
}

type liveActivityRosterRow struct {
	agent activityPaneAgent
	depth int
}

type liveActivityHit struct {
	row, first, last int // One-based terminal coordinates, inclusive.
	agent            string
	skills           bool // Opens the agent's active skills rather than selecting it.
}

type liveActivityRunKey struct {
	first, last uint64
	revision    uint64
	width, clip int
	theme       livediff.Theme
	hover       int // Hovered snippet block in this run, or -1.
	main        bool
	thread      conversationThread // Main transcript thread placement.
	lead        uint64             // Main item a transcript tool group continues, or 0.
	flash       uint64             // Flashed Activity entry in this run, or 0.
	excerpt     bool               // Main sent message or batch shortened out of view.
	tail        int                // Tail lines open output shows; 0 shows the whole tail.
	expansion   uint8
}

func newLiveActivityView() *liveActivityView {
	v := &liveActivityView{
		following: true, status: "CONNECTING",
		painter: activityui.Painter{CopySource: true, Theme: livediff.EnvironmentTheme(os.Getenv("COLORFGBG"))},
	}
	v.painter.Clock = v.now
	return v
}

// apply reports whether the viewer should exit.
func (v *liveActivityView) apply(event activityPaneEvent) bool {
	switch event.Kind {
	case "end":
		return true
	case "coverage":
		v.status = "RECONNECTING"
		return false
	case "snapshot":
		v.status = ""
	case "entries", "agents", "heartbeat":
	default:
		return false
	}
	if event.Agents != nil || event.Kind == "snapshot" {
		if !slices.EqualFunc(v.agents, event.Agents, func(a, b activityPaneAgent) bool { return a.Name == b.Name }) {
			v.hovered = ""
		}
		// Native Activity run headings show roles, which may arrive later.
		if !slices.EqualFunc(v.agents, event.Agents, func(a, b activityPaneAgent) bool { return liveActivityRole(a) == liveActivityRole(b) }) {
			v.runs = nil
		}
		v.agents = event.Agents
	}
	for _, entry := range event.Entries {
		if entry.Seq <= v.lastSeq {
			continue
		}
		v.lastSeq = entry.Seq
		if entry.Agent == "Main" && entry.Kind == "text" && entry.native != nil && entry.journal == nil {
			for _, question := range slices.Backward(v.entries) {
				if (question.Agent == "You" || question.Kind == "question_reply") && question.native != nil && question.native.thread == entry.native.thread && question.native.turn == entry.native.turn {
					entry.native.question = question.Seq
					break
				}
			}
		}
		if entry.Kind != "reasoning" && v.mergeNative(entry) {
			continue
		}
		if entry.Kind == "reasoning" {
			if entry.native != nil && entry.native.phase == "discarded" {
				v.removeThinking(entry.native)
				continue
			}
			// These are provider-visible summaries from the collector, never
			// raw reasoning. Keep legacy panes and the other audience excluded.
			if !(v.childrenOnly && entry.Agent != "/root" || v.conversation && entry.Agent == "Main") {
				continue
			}
			updated := false
			if entry.CallID != "" {
				for i, previous := range v.entries {
					if previous.Kind != "reasoning" || previous.Agent != entry.Agent {
						continue
					}
					// The request's pending block keeps its row and start.
					replaced := entry.native != nil && entry.native.replaces != "" && previous.native != nil &&
						previous.native.thread == entry.native.thread && previous.native.item == entry.native.replaces
					if replaced || previous.CallID == entry.CallID &&
						(previous.native == nil && entry.native == nil || previous.native != nil && entry.native != nil && previous.native.sameItem(entry.native)) {
						entry.Seq = previous.Seq
						if replaced || activityui.ReasoningSummaryHeader(previous.Text) == activityui.ReasoningSummaryHeader(entry.Text) {
							entry.Observed = previous.Observed
						}
						v.replaceEntry(i, entry, parseLiveActivity(entry))
						updated = true
						break
					}
				}
			}
			if updated {
				continue
			}
		}
		if entry.Kind == "exit" {
			for i, v0 := range slices.Backward(v.entries) {
				if v0.Agent == entry.Agent && v0.CallID == entry.CallID && entry.CallID != "" {
					code, _ := strconv.Atoi(entry.Text)
					blocks := commandExitBlocks(v.entries[i].blocks, code, entry.outputTail, entry.outputOmit)
					setCommandTiming(blocks, v0.activityPaneEntry)
					v.replaceEntry(i, v0.activityPaneEntry, blocks)
					break
				}
			}
			continue
		}
		blocks := parseLiveActivity(entry)
		if entry.Kind == "tool" && entry.CallID != "" && len(blocks) > 0 &&
			slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, blocks[0].Verb) {
			// A confirmed receipt replaces the provisional Run or requested
			// Edit row for this call. Only the capturer supplies saved counts.
			for i := len(v.entries) - 1; i >= 0; i-- {
				prior := v.entries[i].activityPaneEntry
				if prior.Kind != "tool" || prior.Agent != entry.Agent || prior.CallID != entry.CallID ||
					len(v.entries[i].blocks) == 0 || (v.entries[i].blocks[0].Verb != "Run" && v.entries[i].blocks[0].Verb != "Edit") {
					continue
				}
				entry.Seq = prior.Seq
				blocks[0].ExitCode = v.entries[i].blocks[0].ExitCode
				for _, annotation := range v.entries[i].blocks[1:] {
					if annotation.BatchExit {
						blocks = retainBatchResult(blocks, annotation)
					} else if annotation.Kind == "filter" {
						blocks = append(blocks, annotation)
					}
				}
				v.replaceEntry(i, entry, blocks)
				blocks = nil
				break
			}
			if blocks == nil {
				continue
			}
		}
		v.appendEntry(entry, blocks)
		if v.standalone(entry) {
			if v.events == nil {
				v.events = make(map[string]liveActivityEvent)
			}
			v.events[entry.Agent] = liveActivityEvent{entry.Seq, cmp.Or(entry.Observed, v.now())}
		}
		if !v.following && v.visible(entry) {
			v.unseen++
		}
	}
	if extra := len(v.entries) - liveActivityFeedLimit; extra > 0 {
		for i := 0; extra > 0 && i < len(v.entries); {
			if n := v.entries[i].native; n != nil && n.running {
				i++
				continue
			}
			v.retireSkills(v.entries[i : i+1])
			delete(v.passed, v.entries[i].Seq)
			v.removeEntries(i, i+1)
			extra--
		}
		for seq := range v.passed {
			if v.historyOrder && !slices.ContainsFunc(v.entries, func(entry liveActivityRecord) bool { return entry.Seq == seq }) || !v.historyOrder && seq < v.entries[0].Seq {
				delete(v.passed, seq)
			}
		}
	}
	v.trackPace(v.now())
	v.keepSelection()
	return false
}

// trackPace starts pacing new live invocations that report several
// operations at once. Restored history and in-place updates show at once.
func (v *liveActivityView) trackPace(now time.Time) {
	for i := len(v.entries) - 1; i >= 0 && v.entries[i].Seq > v.pacedSeq; i-- {
		entry := v.entries[i].activityPaneEntry
		if entry.Kind != "tool" || entry.native == nil || !entry.native.live || len(v.entries[i].blocks) < 2 {
			continue
		}
		if v.paced == nil {
			v.paced = make(map[uint64]liveActivityPace)
		}
		v.paced[entry.Seq] = liveActivityPace{shown: 1, next: now.Add(liveActivityPaceStep)}
	}
	v.pacedSeq = max(v.pacedSeq, v.lastSeq)
}

// pace reveals the next operation of each paced invocation that is due.
func (v *liveActivityView) pace(now time.Time) bool {
	changed := false
	for seq, pace := range v.paced {
		i := slices.IndexFunc(v.entries, func(entry liveActivityRecord) bool { return entry.Seq == seq })
		if i >= 0 && now.Before(pace.next) {
			continue
		}
		// A late frame catches up on every step that fell due.
		for i >= 0 && pace.shown < len(v.entries[i].blocks) && !now.Before(pace.next) {
			pace.shown, pace.next, changed = pace.shown+1, pace.next.Add(liveActivityPaceStep), true
			v.invalidateEntry(seq)
		}
		if i < 0 || pace.shown >= len(v.entries[i].blocks) {
			delete(v.paced, seq)
		} else {
			v.paced[seq] = pace
		}
	}
	return changed
}

// shownBlocks is an entry's blocks as far as its pace has revealed them.
// A pane shorter than liveActivityCompactHeight rows shows only the last
// liveActivityCompactTail lines of open output, so a few commands' output
// cannot fill a small screen.
const (
	liveActivityCompactHeight = 40
	liveActivityCompactTail   = 3
)

// tailRows is how many output lines an open tail shows at the pane's height;
// 0 shows the whole tail.
func (v *liveActivityView) tailRows() int {
	if v.height > 0 && v.height < liveActivityCompactHeight {
		return liveActivityCompactTail
	}
	return 0
}

func (v *liveActivityView) shownBlocks(i int) []activityui.Block {
	if pace, ok := v.paced[v.entries[i].Seq]; ok && pace.shown < len(v.entries[i].blocks) {
		return v.entries[i].blocks[:pace.shown]
	}
	return v.entries[i].blocks
}

// keepSelection falls back to the first agent when the selection is unknown.
func (v *liveActivityView) keepSelection() {
	rows := v.feedAgents()
	if v.selected == "" || !slices.ContainsFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected }) {
		v.selected = ""
		if len(rows) > 0 {
			v.selected = rows[0].agent.Name
		}
	}
}

// feedAgents are the roster rows the feed can filter to. Native Activity
// never shows Main's entries, so it neither counts nor selects Main.
func (v *liveActivityView) feedAgents() []liveActivityRosterRow {
	rows := v.roster()
	if v.childrenOnly {
		rows = slices.DeleteFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == "/root" })
	}
	return rows
}

// unreturnedOutputNote marks command output its exec cell never returned.
const unreturnedOutputNote = "output not returned to the model"

// markUnreturned notes a command's output that its exec cell never
// returned, so the row does not imply the model saw it. A command with no
// output has nothing to note. The note is a filter annotation, which a later
// receipt for the same call keeps.
func (v *liveActivityView) markUnreturned(thread, call string) bool {
	for i, entry := range v.entries {
		if entry.Kind != "tool" || entry.native == nil || entry.native.thread != thread || entry.native.item != call {
			continue
		}
		output, noted := false, false
		for _, block := range v.entries[i].blocks {
			output = output || len(block.Tail)+block.TailOmitted > 0
			noted = noted || block.Kind == "filter" && block.Body == unreturnedOutputNote
		}
		if !output || noted {
			return false
		}
		v.replaceEntry(i, entry.activityPaneEntry, append(v.entries[i].blocks, activityui.Block{Kind: "filter", Body: unreturnedOutputNote}))
		return true
	}
	return false
}

// removeThinking deletes a pending thinking block no reasoning took over.
func (v *liveActivityView) removeThinking(native *liveActivityNativeItem) {
	for i, entry := range v.entries {
		if entry.Kind == "reasoning" && native.sameItem(entry.native) {
			v.removeEntries(i, i+1)
			return
		}
	}
}

func (v *liveActivityView) visible(entry activityPaneEntry) bool {
	if entry.native != nil && entry.native.phase == "queued-summary" {
		return false
	}
	if entry.Kind == "question_reply" {
		return false
	}
	// Wait lifecycle belongs in the roster's latest summary, not either feed.
	// Keep the entries so live updates and restored history share that summary.
	if entry.native != nil && entry.native.wait != nil {
		return false
	}
	if entry.Kind == "tool" && entry.Text == "" {
		return false
	}
	if v.expansion == 0 && entry.Kind == "hook" { // Expanded presentations disclose hook runs.
		return false
	}
	if entry.Kind == "reasoning" {
		if v.conversation {
			return entry.Agent == "Main"
		}
		if !v.childrenOnly {
			return false
		}
	}
	if v.childrenOnly && entry.Agent == "/root" {
		return false
	}
	return !v.only || entry.Agent == v.selected
}

// roster orders agents as a canonical-path tree. Siblings keep observation
// order; an agent whose parent is unobserved is shown at the top level.
func (v *liveActivityView) roster() []liveActivityRosterRow {
	known := make(map[string]bool, len(v.agents))
	for _, agent := range v.agents {
		known[agent.Name] = true
	}
	parent := func(name string) string {
		if i := strings.LastIndex(name, "/"); i > 0 && known[name[:i]] {
			return name[:i]
		}
		return ""
	}
	var rows []liveActivityRosterRow
	var visit func(string, int)
	visit = func(under string, depth int) {
		for _, agent := range v.agents {
			if parent(agent.Name) == under {
				rows = append(rows, liveActivityRosterRow{agent: agent, depth: depth})
				visit(agent.Name, depth+1)
			}
		}
	}
	visit("", 0)
	return rows
}

func (v *liveActivityView) latest(name string) int {
	for i, entry := range slices.Backward(v.entries) {
		if entry.Agent == name {
			return i
		}
	}
	return -1
}

// glyph shows only observed facts: an open provider response, a latest error
// event, or a sent plaintext final answer. None of them claims completion.
func (v *liveActivityView) glyph(agent activityPaneAgent) string {
	if role := liveActivityRole(agent); role != "" {
		return v.roleColor(role) + string(v.agentStatus(agent)) + activityui.Reset
	}
	switch v.agentStatus(agent) {
	case '◐':
		return activityui.Amber + "◐" + activityui.Reset
	case '!':
		return activityui.Red + "!" + activityui.Reset
	case '✓':
		return activityui.Green + "✓" + activityui.Reset
	}
	return activityui.Dim + "·" + activityui.Undim
}

// agentStatus is the glyph's fact; counts use it so they agree with visible rows.
func (v *liveActivityView) agentStatus(agent activityPaneAgent) rune {
	latest := v.latest(agent.Name)
	switch {
	case agent.Responding:
		return '◐'
	case latest >= 0 && v.entries[latest].Kind == "error":
		return '!'
	case agent.Final:
		return '✓'
	}
	return '·'
}

// statusCounts counts responding and error glyphs among rows.
func (v *liveActivityView) statusCounts(rows []liveActivityRosterRow) (responding, errors int) {
	for _, row := range rows {
		switch v.agentStatus(row.agent) {
		case '◐':
			responding++
		case '!':
			errors++
		}
	}
	return responding, errors
}

func (v *liveActivityView) selectAgent(step int) {
	rows := v.feedAgents()
	if len(rows) == 0 {
		return
	}
	index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
	index = (max(0, index) + step + len(rows)) % len(rows)
	v.selected = rows[index].agent.Name
	v.rosterManual = false
	v.hovered = ""
	if v.only {
		v.follow()
	}
}

func (v *liveActivityView) handleMouse(action byte, row, column int) bool {
	if action == 'j' || action == 'k' {
		v.questionHover = 0 // Scrolling moves the link from under the pointer.
		v.editHover = 0
		if !v.feedOnly && row >= v.rosterTop && row <= v.rosterBottom && column >= 1 && column <= v.rosterRight {
			if v.rosterTop == v.rosterBottom {
				return true
			}
			step := 1
			if action == 'k' {
				step = -1
			}
			v.scrollRoster(step)
			return true
		}
		return v.scrollKey(terminalui.PaneWheelKey(action))
	}

	if action != 'h' && action != '\r' {
		return false
	}
	if action == '\r' && column >= v.feedLeft && column <= v.feedRight {
		index := row - v.feedTop
		if index >= 0 && index < len(v.feedQuestions) && v.feedQuestions[index] != 0 {
			if target, ok := v.questionRows[v.feedQuestions[index]]; ok {
				v.offset, v.following = target, false
				v.flashQuestion = v.feedQuestions[index]
				v.flashUntil = v.now().Add(700 * time.Millisecond)
				return true
			}
		}
	}
	link := v.pointQuestion(row, column)
	snippet := v.pointSnippet(action, row, column)
	agent := false
	if !v.feedOnly {
		agent = v.pointAgent(action, row, column)
	}
	return link || snippet || agent
}

// pointQuestion underlines the question link under the pointer.
func (v *liveActivityView) pointQuestion(row, column int) bool {
	hover := 0
	if index := row - v.feedTop; index >= 0 && index < len(v.feedQuestions) && v.feedQuestions[index] != 0 && column >= v.feedLeft && column <= v.feedRight {
		hover = index + 1
	}
	previous := v.questionHover
	v.questionHover = hover
	return hover != previous
}

// underlineLink underlines a question link from its arrow on, leaving the
// gutter plain.
func underlineLink(line string) string {
	lead, link, ok := strings.Cut(line, "↩")
	if !ok {
		return line
	}
	return lead + "\x1b[4m" + strings.ReplaceAll("↩"+link, activityui.Reset, activityui.Reset+"\x1b[4m") + "\x1b[24m"
}

// pointSnippet underlines a hovered snippet's count. A click on an operation
// asks to open its full content in the shared dialog.
func (v *liveActivityView) pointSnippet(action byte, row, column int) bool {
	var snippet liveActivitySnippet
	editHover := 0
	if index := row - v.feedTop; index >= 0 && index < len(v.feedSnippets) && column >= v.feedLeft && column <= v.feedRight {
		snippet = v.feedSnippets[index]
		if snippet.block == editNavigationSnippet {
			editHover = index + 1
		}
	}
	redraw := editHover != v.editHover
	v.editHover = editHover
	if action == '\r' && snippet != (liveActivitySnippet{}) {
		v.opening = snippet
		redraw = true
	}
	if snippet != v.snippet {
		v.snippet, redraw = snippet, true
	}
	return redraw
}

// clickTarget prepares a block whose rows a click acts on, and reports whether
// they do: operations, compact reasoning and collapsed narrative text open the shared dialog.
// Under the pointer, the block underlines its count.
func (v *liveActivityView) clickTarget(block *activityui.Block, snippet liveActivitySnippet, width int) bool {
	if block.Kind == "summary" || block.Kind == "error" {
		details := block.Kind == "summary" && v.painter.ReasoningElided(*block, width) || block.Kind == "error" && activityui.ErrorHasDetails(*block)
		block.Hovered = details && v.snippet == snippet
		return details
	}
	if outputBlock(*block) || block.Kind == "progress" && block.Label != "" && strings.TrimSpace(block.Body) != "" {
		block.Hovered = v.snippet == snippet
		return true
	}
	if !block.Collapsed {
		return false
	}
	block.Hovered = block.Collapsed && v.snippet == snippet
	return true
}

// outputBlock reports an operation the output dialog opens: one with output,
// still running, carrying source a row may clip, or standing for several
// reads. Edits open their captured change instead, and questions their dock.
func outputBlock(block activityui.Block) bool {
	if block.Kind != "op" && block.Kind != "reads" || block.EditSource != "" || len(block.Questions) > 0 {
		return false
	}
	return block.Output != nil || len(block.Tail)+block.TailOmitted > 0 || len(block.Changes) > 0 || block.Code != "" || len(block.Members) > 0
}

// snippetBlock is the block a snippet names in the last frame.
func (v *liveActivityView) snippetBlock(snippet liveActivitySnippet) (activityui.Block, bool) {
	for key, run := range v.runs {
		if key.expansion == v.expansion && key.first == snippet.run && snippet.block >= 0 && snippet.block < len(run.blocks) {
			return run.blocks[snippet.block], true
		}
	}
	return activityui.Block{}, false
}

// pointAgent highlights a hovered roster agent; a click filters the feed.
func (v *liveActivityView) pointAgent(action byte, row, column int) bool {
	previous, previousSkills := v.hovered, v.skillsHover
	v.hovered, v.skillsHover = "", ""
	for _, hit := range v.hits {
		if hit.row == row && column >= hit.first && column <= hit.last {
			if hit.skills {
				v.skillsHover = hit.agent
			} else {
				v.hovered = hit.agent
			}
			if action == '\r' && hit.skills {
				v.skillsRequest, v.skillsHover = hit.agent, ""
				return true
			}
			if action == '\r' {
				if v.childrenOnly && hit.agent == "/root" {
					// Main's activity lives in Main; picking it shows every child.
					v.only = false
				} else {
					v.only = !v.only || v.selected != hit.agent
					v.selected = hit.agent
				}
				v.rosterManual = false
				v.hovered = ""
				v.follow()
			}
			return action == '\r' || previous != v.hovered || previousSkills != v.skillsHover
		}
	}
	return previous != "" || previousSkills != ""
}

func (v *liveActivityView) follow() {
	v.following, v.unseen = true, 0
}

func (v *liveActivityView) scrollKey(key byte) bool {
	offset := v.offset
	if v.following {
		offset = max(0, v.feedLines-v.feedRows)
	}
	next, follow, ok := terminalui.PaneScroll(key, offset, v.feedRows, v.feedLines)
	if ok {
		v.offset = max(0, min(next, v.feedLines-v.feedRows))
		v.following = follow || v.offset == max(0, v.feedLines-v.feedRows)
		if v.following {
			v.unseen = 0
		}
	}
	return ok
}

// render lays the pane out for its size: agent cards beside the feed on wide
// panes, a roster above the feed on narrower ones, and a one-line strip when
// rows are scarce. Every row fits within width-1 columns.
func (v *liveActivityView) render(width, height int, now time.Time) []string {
	width = max(10, width)
	if v.conversation {
		height = max(1, height)
	} else {
		height = max(3, height)
	}
	if width != v.width || height != v.height {
		v.hovered, v.snippet, v.questionHover = "", liveActivitySnippet{}, 0
		v.width, v.height = width, height
	}
	v.hits = v.hits[:0]
	v.feedSnippets = nil
	v.feedQuestions = nil
	v.rosterTop, v.rosterBottom, v.rosterRight = 0, 0, 0
	text := max(1, width-1)
	rows := v.roster()
	footer := height >= 10 && !v.conversation && (!v.childrenOnly || v.focused)
	body := height - 1
	if footer {
		body--
	}
	lines := []string{v.header(rows, text)}
	v.feedTop, v.feedLeft, v.feedRight = 2, 1, text
	if v.conversation || v.bare {
		// Main's composer border or the pane title carries the state; the
		// feed takes every row.
		body, lines, v.feedTop, footer = height, nil, 1, false
	}
	switch {
	case v.feedOnly:
		hint := !v.following && body > 1
		feedRows := body
		if hint {
			feedRows--
		}
		feed := v.renderFeed(text, feedRows)
		lines = append(lines, v.viewport(feed, feedRows)...)
		if hint {
			label := "↓ Back to bottom · esc"
			lines = append(lines, liveActivityHint(v.painter.Theme, label, text))
		}
	case len(rows) > 0 && text >= liveActivitySideColumns && body >= 6:
		cardWidth := min(44, max(28, text*3/10))
		feedWidth := text - cardWidth - 3
		cards := v.renderCards(rows, cardWidth, body, now)
		v.rosterTop, v.rosterBottom, v.rosterRight = 2, body+1, cardWidth
		feed := v.viewport(v.renderFeed(feedWidth, body), body)
		v.feedLeft = cardWidth + 4
		for i := range body {
			lines = append(lines, liveActivityPad(cards[i], cardWidth)+activityui.Dim+" │ "+activityui.Undim+feed[i])
		}
	case len(rows) > 0 && body >= 8:
		roster := v.renderRoster(rows, text, max(2, body/3), now)
		v.rosterTop, v.rosterBottom, v.rosterRight = 2, len(roster)+1, text
		feedRows := body - len(roster) - 1
		lines = append(lines, roster...)
		lines = append(lines, activityui.Dim+strings.Repeat("─", text)+activityui.Undim)
		v.feedTop = len(lines) + 1
		lines = append(lines, v.viewport(v.renderFeed(text, feedRows), feedRows)...)
	case len(rows) > 0:
		v.rosterTop, v.rosterBottom, v.rosterRight = 2, 2, text
		lines = append(lines, v.renderStrip(rows, text))
		v.feedTop = 3
		lines = append(lines, v.viewport(v.renderFeed(text, body-1), body-1)...)
	default:
		lines = append(lines, v.viewport(v.renderFeed(text, body), body)...)
	}
	if footer {
		lines = append(lines, v.footer(text))
	}
	v.copyRows = make([][]activityui.CopySpan, len(lines))
	for row := range lines {
		lines[row], v.copyRows[row] = activityui.ExtractCopy(lines[row])
	}
	return lines
}

func liveActivityHint(theme livediff.Theme, label string, width int) string {
	label = ansi.Truncate(label, width, "")
	return strings.Repeat(" ", max(0, (width-ansi.StringWidth(label))/2)) + theme.Accent() + label + activityui.Reset
}

func liveActivityPad(line string, width int) string {
	line = ansi.Truncate(line, width, "…")
	return line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))
}

func (v *liveActivityView) header(rows []liveActivityRosterRow, width int) string {
	return v.renderHeader(rows, width, false)
}

func (v *liveActivityView) renderHeader(rows []liveActivityRosterRow, width int, rosterOnly bool) string {
	title := "AGENTS"
	if v.feedOnly && !rosterOnly {
		title = "ACTIVITY"
	}
	if v.conversation && !rosterOnly {
		title = "MAIN"
	}

	if v.childrenOnly && !rosterOnly {
		return v.activityHeader(width)
	}
	left := "\x1b[1m" + v.painter.Theme.Accent() + title + activityui.Reset
	responding, errors := v.statusCounts(rows)
	switch {
	case v.feedOnly && !rosterOnly && !v.only:
		left += " · all"
	case len(rows) == 0:
		left += activityui.Dim + " · waiting for subagent activity" + activityui.Undim
	case v.only && !rosterOnly:
		index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
		left += fmt.Sprintf(" · only %s %s(%d/%d)%s", v.painter.Agent(v.selected), activityui.Dim, index+1, len(rows), activityui.Undim)
	default:
		left += fmt.Sprintf("  %d agents · %d responding", len(rows), responding)
		if errors > 0 {
			left += fmt.Sprintf(" · %d error", errors)
		}
	}
	if rosterOnly {
		return ansi.Truncate(left, width, "…")
	}
	right := "FOLLOW"
	if !v.following {
		right = activityui.Amber + "PAUSED" + activityui.Reset
		if v.unseen > 0 {
			right += fmt.Sprintf(" · %d new", v.unseen)
		}
	}

	if v.status != "" {
		right = v.status
	}
	if right == "" {
		return ansi.Truncate(left, width, "…")
	}
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 2 {
		return ansi.Truncate(left, max(0, width-ansi.StringWidth(right)-1), "…") + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

// activityHeader names the native feed and its filter. Following the live
// edge is the default, so only a paused feed says so.
func (v *liveActivityView) activityHeader(width int) string {
	left := "\x1b[1m" + v.painter.Theme.Accent() + "Activity" + activityui.Reset
	if v.only {
		rows := v.feedAgents()
		index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
		left += activityui.Dim + " · only " + activityui.Undim + v.painter.Agent(v.selected) + activityui.Dim + fmt.Sprintf(" %d/%d", index+1, len(rows)) + activityui.Undim
	}
	right := v.status
	if right == "" && !v.following {
		right = activityui.Amber + "paused" + activityui.Reset
		if v.unseen > 0 {
			right += activityui.Amber + fmt.Sprintf(" · %d new", v.unseen) + activityui.Reset
		}
		right += activityui.Dim + " · End follows" + activityui.Undim
	}
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if right == "" || gap < 2 {
		return ansi.Truncate(left, width, "…")
	}
	return left + strings.Repeat(" ", gap) + right
}

// current is an agent's latest activity summary and its elapsed/response timer.
func (v *liveActivityView) current(agent activityPaneAgent, now time.Time, widths ...int) (string, string) {
	width := 80 // State/timer queries before a roster has display geometry.
	if len(widths) > 0 {
		width = max(1, widths[0])
	}
	summary := activityui.Dim + "—" + activityui.Undim
	source := v
	if agent.Name == "/root" && v.mainView != nil {
		source = v.mainView
		if source.status != "" {
			summary = source.status
		}
	}
	for i, v0 := range slices.Backward(source.entries) {
		blocks := source.shownBlocks(i)
		if len(blocks) == 0 {
			continue
		}
		if v0.Agent == agent.Name || agent.Name == "/root" && source != v && v0.Agent == "Main" {
			if v0.Kind == "reasoning" {
				// Roster status is per agent, even when other agents interleave.
				var earlier []activityui.Block
				for j := i - 1; j >= 0; j-- {
					previous := source.entries[j].activityPaneEntry
					if previous.Agent != v0.Agent {
						continue
					}
					if previous.Kind != "reasoning" {
						break
					}
					earlier = append(earlier, source.shownBlocks(j)...)
				}
				slices.Reverse(earlier)
				blocks = append(earlier, blocks...)
			}
			summary = v.painter.Summary(blocks, width)
			if v0.Kind == "reasoning" && agent.Responding {
				summary = activityui.ReasoningShimmer(ansi.Strip(summary), now.Sub(v0.Observed), v.painter.Colors)
			}
			break
		}
		if agent.Name == "/root" && len(blocks) == 1 && blocks[0].Kind == "message" && blocks[0].To == "/root" {
			block := blocks[0]
			block.Owner = "/root"
			summary = v.painter.Summary([]activityui.Block{block}, width)
			break
		}
	}
	// Before its first response, an agent's last activity is its start.
	last := cmp.Or(agent.LastResponse, agent.Started)
	if agent.Started.IsZero() || !agent.WorkTimer.Known {
		return summary, ""
	}
	// The response-age clock is independent of accumulated active work.
	return summary, liveActivityAge(agent.WorkTimer.at(now)) + " · " + liveActivityLast(last, now)
}

// liveActivityTokens shows cumulative input (sent) and output (received) tokens.
func liveActivityTokens(agent activityPaneAgent) string {
	if !agent.TokensKnown && agent.InputTokens == 0 && agent.OutputTokens == 0 {
		return ""
	}
	input, output := liveActivityTokenParts(agent)
	return "↑ " + input + " ↓ " + output
}

func liveActivityTokenParts(agent activityPaneAgent) (string, string) {
	prefix := ""
	if agent.UsagePartial {
		prefix = "≥"
	}
	return prefix + formatUsageTokens(agent.InputTokens), prefix + formatUsageTokens(agent.OutputTokens)
}

// liveActivityCost omits unknown cost rather than claiming zero; a response
// that ended without usage makes the observed cost a lower bound.
func liveActivityCost(agent activityPaneAgent) string {
	if agent.Turns == 0 || !agent.CostKnown {
		return ""
	}
	if agent.CostPartial {
		return fmt.Sprintf("≥$%.2f", agent.Cost)
	}
	return fmt.Sprintf("$%.2f", agent.Cost)
}

// liveActivityTurns counts provider roundtrips as T+N.
func liveActivityTurns(agent activityPaneAgent) string {
	if agent.Roundtrips == 0 {
		return ""
	}
	prefix := ""
	if agent.RoundtripsPartial {
		prefix = "≥"
	}
	return activityui.Dim + prefix + "T+" + activityui.Undim + fmt.Sprint(agent.Roundtrips)
}

// selectRow fills the selected agent's rows, so selection takes no column of
// its own. Resets inside the row restore the fill.
func (v *liveActivityView) selectRow(line string, width int) string {
	fill := v.painter.Theme.SelectionBackground()
	line = strings.ReplaceAll(line, activityui.Reset, activityui.Reset+fill)
	return fill + line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line))) + "\x1b[49m"
}

func (v *liveActivityView) hoverName(name, agent string) string {
	if agent == v.hovered {
		return activityui.Underline(name)
	}
	return name
}

// renderCards gives the name, full-width activity, and short metrics separate rows.
func (v *liveActivityView) renderCards(rows []liveActivityRosterRow, width, height int, now time.Time) []string {
	lines := v.renderAgentRows(rows, width, height, now, true)
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func (v *liveActivityView) renderRoster(rows []liveActivityRosterRow, width, limit int, now time.Time) []string {
	return v.renderAgentRows(rows, width, limit, now, false)
}

func (v *liveActivityView) scrollRoster(step int) {
	if step > 0 && v.rosterEnd >= len(v.agents) {
		return
	}
	v.rosterOffset = max(0, min(v.rosterOffset+step, len(v.agents)-1))
	v.rosterManual, v.hovered = true, ""
}

// rosterRange gives compact agent rows priority over secondary metrics.
// Overflow indicators consume space only when there are hidden agents.
func rosterRange(count, start, selected, limit int) (end int, metrics bool) {
	remaining := limit
	if start > 0 && limit >= 3 {
		remaining--
	}
	end = start
	for end < count {
		bottom := 0
		if end+1 < count && limit >= 3 {
			bottom = 1
		}
		if remaining < 1+bottom {
			break
		}
		remaining--
		end++
	}
	if end < count && limit >= 3 {
		remaining--
	}
	return end, remaining > 0 && selected >= start && selected < end
}

// rosterTree draws guides only from ancestors that are drawn as branches, so
// a row whose parent is off-screen shows its relative path without guides.
func rosterTree(rows []liveActivityRosterRow, index, start int) (name, indent string) {
	parentAt := func(i int) int {
		parent, _, _ := strings.CutLast(rows[i].agent.Name, "/")
		for j := i - 1; j >= start; j-- {
			if rows[j].agent.Name == parent {
				return j
			}
		}
		return -1
	}
	continuation := func(i int) bool {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].depth < rows[i].depth {
				break
			}
			if rows[j].depth == rows[i].depth {
				return true
			}
		}
		return false
	}
	parent := parentAt(index)
	if rows[index].depth == 0 || parent < 0 {
		return activityui.AgentDisplayName(rows[index].agent.Name), ""
	}
	var guides []string
	for i := parent; i >= 0; i = parentAt(i) {
		if rows[i].depth == 0 || parentAt(i) < 0 {
			break
		}
		guide := "  "
		if continuation(i) {
			guide = "│ "
		}
		guides = append(guides, guide)
	}
	slices.Reverse(guides)
	prefix := strings.Join(guides, "")
	branch, guide := "└ ", "  "
	if continuation(index) {
		branch, guide = "├ ", "│ "
	}
	_, leaf, _ := strings.CutLast(rows[index].agent.Name, "/")
	return prefix + branch + leaf, prefix + guide
}

func (v *liveActivityView) hiddenRoster(rows []liveActivityRosterRow, direction string) string {
	responding, errors := v.statusCounts(rows)
	line := fmt.Sprintf("%s %d more", direction, len(rows))
	if responding > 0 {
		line += fmt.Sprintf(" · %d ◐", responding)
	}
	if errors > 0 {
		line += fmt.Sprintf(" · %d !", errors)
	}
	return activityui.Dim + line + activityui.Undim
}

// renderAgentRows gives each roster agent one row with its metrics inline;
// cards give the name, activity, and short metrics separate rows.
func (v *liveActivityView) renderAgentRows(rows []liveActivityRosterRow, width, limit int, now time.Time, cards bool) []string {
	if limit <= 0 || len(rows) == 0 {
		return nil
	}
	selected := max(0, slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected }))
	stride := 1
	if cards {
		stride = 3
	}
	compact := len(rows)*stride > limit
	start, end, metrics := 0, len(rows), false
	if compact {
		start = min(v.rosterOffset, len(rows)-1)
		if v.rosterSelected != v.selected {
			v.rosterManual = false
		}
		if !v.rosterManual {
			start = min(start, selected)
		}
		end, metrics = rosterRange(len(rows), start, selected, limit)
		for !v.rosterManual && selected >= end && start < selected {
			start++
			end, metrics = rosterRange(len(rows), start, selected, limit)
		}
	}
	v.rosterOffset, v.rosterEnd, v.rosterSelected = start, end, v.selected
	names, indents := make([]string, end-start), make([]string, end-start)
	nameWidth := 1
	for i := range rows {
		// Full-tree widths keep the summary column still while the window scrolls.
		name, _ := rosterTree(rows, i, 0)
		nameWidth = max(nameWidth, ansi.StringWidth(name))
	}
	for i := start; i < end; i++ {
		name, indent := rosterTree(rows, i, start)
		names[i-start], indents[i-start] = name, indent
		nameWidth = max(nameWidth, ansi.StringWidth(name))
	}
	nameWidth = min(nameWidth, max(1, width/3))
	var table []string
	if !cards {
		table = v.metricTable(rows[start:end], now)
	}
	var lines []string
	add := func(line string) { lines = append(lines, ansi.Truncate(line, width, "…")) }
	if start > 0 && limit >= 3 {
		add(v.hiddenRoster(rows[:start], "↑"))
	}
	for i := start; i < end; i++ {
		row := rows[i]
		first := len(lines) + 2
		name := names[i-start]
		available := nameWidth
		if cards && !compact {
			available = max(1, width-3)
		}
		name = strings.TrimRight(liveActivityMiddle(name, available), " ")
		split := strings.LastIndexAny(name, " /") + 1
		styled := activityui.DimColor(row.agent.Name) + name[:split] + activityui.Undim + activityui.Color(row.agent.Name) + v.hoverName(name[split:], row.agent.Name) + activityui.Reset
		prefix := " " + v.glyph(row.agent) + " "
		switch {
		case cards && !compact:
			last := liveActivityLast(cmp.Or(row.agent.LastResponse, row.agent.Started), now)
			gap := width - 3 - ansi.StringWidth(name) - ansi.StringWidth(last)
			line := prefix + styled
			if gap >= 2 {
				line += strings.Repeat(" ", gap) + activityui.Dim + last + activityui.Undim
			}
			add(line)
			summary, _ := v.current(row.agent, now, width-3-ansi.StringWidth(indents[i-start]))
			add("   " + activityui.Dim + indents[i-start] + activityui.Undim + summary)
		case cards:
			line := prefix + styled + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name))) + "  "
			summary, _ := v.current(row.agent, now, width-ansi.StringWidth(line))
			add(line + summary)
		default:
			line := prefix + styled + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name))) + "  "
			metric := table[i-start]
			room := width - ansi.StringWidth(line) - 2 - ansi.StringWidth(metric)
			if metric == "" || room < 20 {
				summary, _ := v.current(row.agent, now, width-ansi.StringWidth(line))
				add(line + summary)
			} else {
				summary, _ := v.current(row.agent, now, room)
				add(line + liveActivityPad(summary, room) + "  " + metric)
			}
		}
		if cards && (!compact || (metrics && i == selected)) {
			add("   " + activityui.Dim + indents[i-start] + activityui.Undim + liveActivityCardMetrics(row.agent))
		}
		if i == selected {
			for j := first - 2; j < len(lines); j++ {
				lines[j] = v.selectRow(lines[j], width)
			}
		}
		for hit := first; hit < len(lines)+2; hit++ {
			v.hits = append(v.hits, liveActivityHit{row: hit, first: 1, last: width, agent: row.agent.Name})
		}
	}
	if end < len(rows) && len(lines) < limit {
		add(v.hiddenRoster(rows[end:], "↓"))
	}
	return lines
}

func liveActivityLast(last, now time.Time) string {
	if last.IsZero() {
		return "—"
	}
	age := max(0, now.Sub(last))
	if age < 2*time.Second {
		return "just now"
	}
	return liveActivityAge(age) + " ago"
}

// metricTable reserves stable timer, input/output, cost, and turn slots even
// before values arrive. Large values clip rather than shifting adjacent columns.
func (v *liveActivityView) metricTable(rows []liveActivityRosterRow, now time.Time) []string {
	const columns = 4
	cells := make([][columns]string, len(rows))
	widths := [columns]int{14, 17, 7, 5}
	for i, row := range rows {
		_, timer := v.current(row.agent, now)
		tokens := liveActivityTokens(row.agent)
		if tokens != "" {
			input, output := liveActivityTokenParts(row.agent)
			tokens = activityui.Dim + "↑ " + activityui.Undim + liveActivityPad(input, 6) + activityui.Dim + " ↓ " + activityui.Undim + liveActivityPad(output, 6)
		}
		timerCell := ""
		if timer != "" {
			elapsed, age, _ := strings.Cut(timer, " · ")
			timerCell = strings.Repeat(" ", max(0, 3-len(elapsed))) + liveActivityMetricValues(elapsed) + activityui.Dim + " · " + activityui.Undim + liveActivityMetricValues(age)
		}
		cost, turns := "", ""
		if row.agent.Turns > 0 && row.agent.CostKnown {
			prefix := " "
			if row.agent.CostPartial {
				prefix = "≥"
			}
			cost = prefix + "$" + fmt.Sprintf("%.2f", row.agent.Cost)
		}
		turns = liveActivityTurns(row.agent)
		cells[i] = [columns]string{timerCell, tokens, cost, turns}
	}
	table := make([]string, len(rows))
	for i := range rows {
		var parts []string
		for c, cell := range cells[i] {
			cell = ansi.Truncate(cell, widths[c], "…")
			pad := strings.Repeat(" ", widths[c]-ansi.StringWidth(cell))
			parts = append(parts, cell+pad)
		}
		table[i] = strings.Join(parts, " ")
	}
	return table
}

// liveActivityRole omits the root's redundant role.
func liveActivityRole(agent activityPaneAgent) string {
	if agent.Name == "/root" {
		return ""
	}
	return strings.Join(strings.Fields(livediff.Safe(agent.Role, false)), " ")
}

// Keep numerical values bright while labels and units recede, including
// every unit of a compound duration such as 1m34s.
func liveActivityMetricValues(text string) string {
	var b strings.Builder
	dim := false
	for _, r := range text {
		if value := r >= '0' && r <= '9' || r == '.'; value == dim {
			if dim = !value; dim {
				b.WriteString(activityui.Dim)
			} else {
				b.WriteString(activityui.Undim)
			}
		}
		b.WriteRune(r)
	}
	if dim {
		b.WriteString(activityui.Undim)
	}
	return b.String()
}

func liveActivityCardMetrics(agent activityPaneAgent) string {
	var parts []string
	if cost := liveActivityCost(agent); cost != "" {
		parts = append(parts, cost)
	} else if agent.InputTokens > 0 {
		parts = append(parts, activityui.Dim+"↑ "+activityui.Undim+formatUsageTokens(agent.InputTokens))
	}
	if turns := liveActivityTurns(agent); turns != "" {
		parts = append(parts, turns)
	}
	return strings.Join(parts, activityui.Dim+" · "+activityui.Undim)
}

// renderStrip is the one-line roster for short panes.
func (v *liveActivityView) renderStrip(rows []liveActivityRosterRow, width int) string {
	var parts []string
	column := 1
	for _, row := range rows {
		name := activityui.AgentDisplayName(row.agent.Name)
		if row.agent.Name == v.selected || row.agent.Name == v.hovered {
			name = activityui.Underline(name)
		}
		part := v.glyph(row.agent) + " " + activityui.Color(row.agent.Name) + name + activityui.Reset
		if last := min(width, column+ansi.StringWidth(part)-1); column <= last {
			v.hits = append(v.hits, liveActivityHit{row: 2, first: column, last: last, agent: row.agent.Name})
		}
		parts = append(parts, part)
		column += ansi.StringWidth(part) + 2
	}
	return ansi.Truncate(strings.Join(parts, "  "), width, "…")
}

type liveActivityFeed struct {
	spans     []liveActivitySpan
	paints    []liveActivityPaint // Cold runs to decorate if they enter the viewport.
	lines     []string
	heads     []int                 // Index of the heading that owns each line.
	snippets  []liveActivitySnippet // Snippet that owns each line, if any.
	questions []uint64
	passing   []liveActivityPassing
	running   []int // Command headers to keep visible until host completion.
}

type liveActivityPaint struct {
	start  int
	key    liveActivityRunKey
	render func() liveActivityRun
}

type liveActivitySyntaxWindow struct{ first, last int }

// paintBlock measures an independent block without syntax before decorating
// it. Grouped runs can be arbitrarily long; intersection with their heading
// must not tokenize every operation they contain.
func (v *liveActivityView) paintBlock(start, limit int, render func() []string) []string {
	if v.syntaxWindow == nil {
		return render()
	}
	saved := v.painter.LayoutOnly
	v.painter.LayoutOnly = true
	rows := render()
	count := len(rows)
	if limit > 0 {
		count = min(count, limit)
	}
	if start < v.syntaxWindow.last && start+count > v.syntaxWindow.first {
		v.painter.LayoutOnly = false
		rows = render()
	}
	v.painter.LayoutOnly = saved
	return rows
}

// liveActivityPassing is a Main item that shortens once it has scrolled out
// of view: its first entry and the feed row after it.
type liveActivityPassing struct {
	seq uint64
	end int
}

func (feed *liveActivityFeed) separator() {
	feed.heads = append(feed.heads, len(feed.lines))
	feed.lines = append(feed.lines, "")
	feed.snippets = append(feed.snippets, liveActivitySnippet{})
	feed.questions = append(feed.questions, 0)
}

// appendRows keeps row content and metadata aligned; each renderer owns heads.
func (feed *liveActivityFeed) appendRows(run liveActivityRun) {
	for _, row := range run.running {
		feed.running = append(feed.running, len(feed.lines)+row)
	}
	feed.lines = append(feed.lines, run.lines...)
	feed.snippets = append(feed.snippets, run.snippets...)
	feed.questions = append(feed.questions, run.questions...)
}

// renderFeed groups consecutive entries of one agent under a heading with a
// colored gutter. Adjacent reads collapse into one row. In the interleaved
// view each block is clipped to five rows; full content opens in a dialog.
func (v *liveActivityView) renderFeed(width, rows int) liveActivityFeed {
	v.painter.LayoutOnly = true
	feed := v.layoutFeed(width, rows)
	v.painter.LayoutOnly = false
	if anchor := v.expansionAnchor; anchor != nil {
		for _, span := range feed.spans {
			if span.seq == anchor.seq {
				v.offset = span.start + min(anchor.start, max(0, span.end-span.start-1))
				break
			}
		}
		v.expansionAnchor = nil
	}
	offset, _, pins := v.pinnedViewport(feed, rows)
	for _, paint := range feed.paints {
		old := v.runs[paint.key]
		decorate := func(first, last int) {
			if first >= last {
				return
			}
			if old.painted == nil {
				old.painted = make([]bool, len(old.lines))
			}
			if !slices.Contains(old.painted[first:last], false) {
				return
			}
			v.syntaxWindow = &liveActivitySyntaxWindow{first, last}
			run := paint.render()
			v.syntaxWindow = nil
			copy(old.lines[first:last], run.lines[first:last])
			for i := first; i < last; i++ {
				old.painted[i] = true
			}
			copy(feed.lines[paint.start+first:paint.start+last], run.lines[first:last])
		}
		decorate(max(0, offset-paint.start), min(len(old.lines), offset+rows-len(pins)-paint.start))
		for _, pin := range pins {
			if row := pin - paint.start; row >= 0 && row < len(old.lines) {
				decorate(row, row+1)
			}
		}
		old.colored = old.painted != nil && !slices.Contains(old.painted, false)
		v.runs[paint.key] = old
	}
	feed.paints = nil
	return feed
}

func (v *liveActivityView) layoutFeed(width, rows int) liveActivityFeed {
	if v.conversation {
		return v.renderConversation(width)
	}
	clip := 5
	if v.expansion != 0 {
		clip = 0
	}
	// A cross-pane jump flashes its target from the first frame that shows it.
	if v.pendingTarget != 0 && slices.ContainsFunc(v.entries, func(entry liveActivityRecord) bool {
		return entry.Seq == v.pendingTarget && v.visible(entry.activityPaneEntry)
	}) {
		v.flashQuestion, v.flashUntil = v.pendingTarget, v.now().Add(700*time.Millisecond)
	}
	flash := uint64(0)
	if v.now().Before(v.flashUntil) {
		flash = v.flashQuestion
	}
	var feed liveActivityFeed
	v.questionRows = make(map[uint64]int)
	used := make(map[liveActivityRunKey]liveActivityRun)
	lastPreview := make(map[string]int)
	for i, entry := range v.entries {
		if v.visible(entry.activityPaneEntry) && len(v.livePreviews[entry.Agent]) > 0 {
			lastPreview[entry.Agent] = i
		}
	}
	appendPreview := func(agent string) {
		if len(feed.lines) > 0 {
			feed.separator()
		}
		head := len(feed.lines)
		lines := v.livePreviews[agent]
		for range lines {
			feed.heads = append(feed.heads, head)
		}
		feed.appendRows(liveActivityRun{lines: lines, snippets: make([]liveActivitySnippet, len(lines)), questions: make([]uint64, len(lines))})
	}
	for i := 0; i < len(v.entries); {
		if !v.visible(v.entries[i].activityPaneEntry) {
			i++
			continue
		}
		agent := v.entries[i].Agent
		journal := nativeJournalEntry(v.entries[i].activityPaneEntry)
		shared := !journal && v.sharedEvent(i)
		if last, ok := lastPreview[agent]; ok {
			if i == last {
				appendPreview(agent)
			}
			i++
			continue
		}
		last, j := i, i
		for ; j < len(v.entries); j++ {
			if !v.visible(v.entries[j].activityPaneEntry) {
				continue
			}
			next := v.entries[j].activityPaneEntry
			if next.Agent != agent || nativeJournalEntry(next) != journal || !journal && j != i && (shared || v.sharedEvent(j)) {
				break
			}
			if journal && j != i && !(conversationJournalEvent(v.entries[i].activityPaneEntry) && conversationJournalEvent(next) || conversationMilestone(v.entries[i].activityPaneEntry) && conversationMilestone(next)) {
				break
			}
			last = j
		}
		key := liveActivityRunKey{first: v.entries[i].Seq, last: v.entries[last].Seq, revision: v.runRevision(i, last+1), width: width, clip: clip, theme: v.painter.Theme, hover: -1, tail: v.tailRows(), expansion: v.expansion}
		foldJournal := v.entries[i].Kind == "journal_event" || v.entries[i].Kind == "journal_card"
		if foldJournal {
			key.excerpt = v.excerpt(v.passed[key.last])
		}
		if flash != 0 && slices.ContainsFunc(v.entries[i:j], func(entry liveActivityRecord) bool { return entry.Seq == flash }) {
			key.flash = flash
		}
		if v.snippet.run == key.first {
			key.hover = v.snippet.block
		}
		run, ok := v.runs[key]
		for k := i; k <= last; k++ {
			if n := v.entries[k].native; n != nil && n.running {
				ok = false
				break
			}
		}
		first := i // The deferred painter must not borrow the advancing loop cursor.
		render := func() liveActivityRun {
			if journal {
				return v.conversationItem(first, last, width, conversationThread{})
			}
			if shared {
				return v.activityEventItem(first, width, clip, key.flash)
			}
			var blocks []activityui.Block
			for k := first; k <= last; k++ {
				if v.visible(v.entries[k].activityPaneEntry) {
					for _, block := range v.shownBlocks(k) {
						block.Source = v.entries[k].Seq
						block.Flash = block.Source == key.flash
						block.TailRows = key.tail
						blocks = append(blocks, block)
					}
				}
			}
			observed := v.entries[last].Observed
			if v.childrenOnly {
				observed = v.entries[first].Observed // A stable heading while the run grows.
			}
			if v.expansion == 0 {
				blocks = activityui.MergeLiveActivityReads(blocks)
			}
			return v.renderRun(key.first, agent, observed, blocks, width, clip)
		}
		if !ok {
			run = render()
			run.colored = !v.painter.LayoutOnly
		}
		used[key] = run
		if len(feed.lines) > 0 {
			feed.separator()
		}
		head := len(feed.lines)
		if full := key; !ok && key.excerpt && !v.following && head < v.offset {
			full.excerpt = false
			if shown, ok := v.runs[full]; ok {
				v.offset -= len(shown.lines) - len(run.lines)
			}
		}
		if !run.colored {
			feed.paints = append(feed.paints, liveActivityPaint{head, key, render})
		}
		for seq, row := range run.entryRows {
			v.questionRows[seq] = head + row
		}
		for range run.lines {
			feed.heads = append(feed.heads, head)
		}
		feed.appendRows(run)
		feed.spans = append(feed.spans, liveActivitySpan{key.first, head, len(feed.lines)})
		if foldJournal && !key.excerpt {
			feed.passing = append(feed.passing, liveActivityPassing{key.last, len(feed.lines)})
		}
		i = j
	}
	// A preview can arrive before the caller's first transcript entry.
	for _, agent := range slices.Sorted(maps.Keys(v.livePreviews)) {
		if _, ok := lastPreview[agent]; !ok {
			appendPreview(agent)
		}
	}
	v.retainRuns(used)
	return feed
}

func (v *liveActivityView) renderRun(first uint64, agent string, observed time.Time, blocks []activityui.Block, width, clip int) liveActivityRun {
	head := v.painter.Agent(agent)
	detail := ""
	if i := slices.IndexFunc(v.agents, func(a activityPaneAgent) bool { return a.Name == agent }); i >= 0 {
		if role := liveActivityRole(v.agents[i]); role != "" {
			detail = "· " + role
		}
	}
	heading := activityui.EventHeading(activityui.Gutter(agent, v.painter.Theme)+"●"+activityui.Reset, head, detail, observed, width)

	run := liveActivityRun{
		lines:     []string{heading},
		snippets:  make([]liveActivitySnippet, 1),
		questions: make([]uint64, 1),
		entryRows: make(map[uint64]int),
	}
	gutter := activityui.Gutter(agent, v.painter.Theme) + "▎" + activityui.Reset + " "
	if v.childrenOnly {
		gutter = activityui.Gutter(agent, v.painter.Theme) + "│" + activityui.Reset + " "
	}
	previousMessage, previousSummary := false, false
	operation := func(block activityui.Block) bool { return block.Kind == "op" || block.Kind == "reads" }
	// An edit group's later rows and output notes continue the branch above.
	continued := func(block activityui.Block) bool {
		return block.Kind == "filter" || block.GroupHeader != "" && !block.GroupStart
	}
	rail := ""
	blocks = activityui.AlignVerbs(activityui.GroupOperations(blocks))
	run.blocks = blocks
	for index, block := range blocks {
		block = v.discloseBlock(block)
		v.painter.CopyScope = first + uint64(index)
		// Native Activity joins consecutive operations into one tree.
		tree := v.childrenOnly && operation(block) && !continued(block)
		toggle := v.clickTarget(&block, liveActivitySnippet{run: first, block: index}, width-2)
		if block.Kind == "journal" && journalActivityBody(block.Body, true) != block.Body {
			toggle = true // The omitted task row remains available in the dialog.
		}
		if operation(block) && v.expansion != 2 {
			block.SourceRows = 5
			if block.TailRows == 0 || block.TailRows > 5 {
				block.TailRows = 5
			}
		}
		message := slices.Contains([]string{"text", "message", "final", "summary", "start", "journal"}, block.Kind)
		// Reasoning heads the operations after it, so no gap parts them.
		headed := previousSummary && operation(block)
		gap := len(run.lines) > 1 && (message || previousMessage) && !headed
		start := len(run.lines)
		if gap {
			start++
		}
		limit := clip
		if operation(block) {
			limit = 0
		} // Source and output have separate budgets.
		part := v.paintBlock(start, limit, func() []string {
			switch {
			case block.Kind == "journal":
				return v.painter.Flash(block, v.painter.Markdown(journalActivityBody(block.Body, v.expansion == 0), width-2))
			case tree:
				return v.painter.Event(block, width-4)
			case v.childrenOnly:
				return v.painter.Event(block, width-2)
			default:
				return v.painter.Block(block, width-2)
			}
		})
		if len(part) == 0 {
			continue
		}
		if gap {
			run.lines = append(run.lines, gutter)
			run.snippets = append(run.snippets, liveActivitySnippet{})
			run.questions = append(run.questions, 0)
		}
		if block.Running {
			run.running = append(run.running, len(run.lines))
		}
		previousMessage, previousSummary = message, block.Kind == "summary"
		// A merged read row stands for each entry it merged.
		for _, member := range append([]activityui.Block{block}, block.Members...) {
			if member.Source != 0 {
				if _, exists := run.entryRows[member.Source]; !exists {
					run.entryRows[member.Source] = len(run.lines)
				}
			}
		}
		var snippet liveActivitySnippet
		if toggle {
			snippet = liveActivitySnippet{run: first, block: index}
		}
		if limit > 0 && len(part) > limit {
			// An operation shows in full in the output dialog.
			snippet = liveActivitySnippet{run: first, block: index}
			hint := activityui.Elision{Hidden: len(part) - limit + 1, Hovered: snippet == v.snippet}
			part = append(part[:limit-1:limit-1], hint.String())
		}
		if block.EditSource != "" {
			snippet = liveActivitySnippet{run: block.Source, block: editNavigationSnippet, path: activityui.EditPath(block)}
		}
		switch {
		case tree:
			last := true
			for _, next := range blocks[index+1:] {
				if !continued(next) {
					last = !operation(next)
					break
				}
			}
			tail := activityui.Tree([][]string{part, {""}})
			if last {
				tail = activityui.Tree([][]string{part})
			}
			part, rail = tail[:len(part)], "│ "
			if last {
				rail = "  "
			}
		case v.childrenOnly && continued(block) && rail != "":
			for k := range part {
				part[k] = activityui.Dim + rail + activityui.Undim + part[k]
			}
		default:
			rail = ""
		}
		for _, line := range part {
			run.lines = append(run.lines, gutter+ansi.Truncate(line, width-2, "…"))
			run.snippets = append(run.snippets, snippet)
			run.questions = append(run.questions, 0)
		}
	}
	return run
}

// viewportPosition resolves scrolling and pending navigation targets without
// committing viewport geometry.
func (v *liveActivityView) viewportPosition(feed liveActivityFeed, rows int) (int, bool) {
	offset, following := v.offset, v.following
	if target, ok := v.questionRows[v.pendingTarget]; v.pendingTarget != 0 && ok {
		offset, following = max(0, target-1), false
	}
	if following {
		offset = len(feed.lines) - rows
	}
	return max(0, min(offset, len(feed.lines)-rows)), following
}

// pinnedViewport reserves rows for off-screen running headers before resolving
// scrolling. Transcript offsets remain source-row coordinates, including Home
// and navigation targets. Narrowing the body can expose more headers to pin.
func (v *liveActivityView) pinnedViewport(feed liveActivityFeed, rows int) (int, bool, []int) {
	limit := max(0, rows-1)
	if rows == 1 {
		limit = 1
	}
	reserved := 0
	for {
		body := max(0, rows-reserved)
		offset, following := v.viewportPosition(feed, body)
		var pins []int
		for _, row := range feed.running {
			if len(pins) >= limit {
				break
			}
			if row >= offset && row < offset+body {
				continue
			}
			if !v.conversation {
				head := feed.heads[row]
				if !slices.Contains(pins, head) && len(pins)+1 < limit {
					pins = append(pins, head)
				}
			}
			pins = append(pins, row)
		}
		// Activity's current agent heading also needs its own row: replacing
		// the body's first row could erase a naturally visible command header.
		if !v.conversation && len(feed.running) > 0 && offset < len(feed.heads) && len(pins) < limit {
			head := feed.heads[offset]
			// Keep a reserved heading row if following crosses a group boundary;
			// dropping it would widen the body and move back across the boundary.
			if len(pins) < reserved || head != offset && (len(pins) == 0 || feed.heads[pins[len(pins)-1]] != head) {
				pins = append(pins, head)
			}
		}
		if len(pins) == reserved {
			return offset, following, pins
		}
		reserved = len(pins)
	}
}

// viewport returns exactly rows lines. When scrolled into a run, that run's
// heading stays pinned on the first row.
func (v *liveActivityView) viewport(feed liveActivityFeed, rows int) []string {
	rows = max(0, rows)
	var pins []int
	v.offset, v.following, pins = v.pinnedViewport(feed, rows)
	v.pendingTarget = 0
	v.feedSpans = feed.spans
	v.feedLines, v.feedRows = len(feed.lines), max(1, rows-len(pins))
	if v.following {
		v.unseen = 0
	}
	for _, item := range feed.passing {
		if item.end <= v.offset {
			if v.passed == nil {
				v.passed = make(map[uint64]bool)
			}
			v.passed[item.seq] = true
		}
	}
	lines := make([]string, rows)
	v.feedSnippets = make([]liveActivitySnippet, rows)
	v.feedQuestions = make([]uint64, rows)
	for row := range rows {
		index := v.offset + row - len(pins)
		if row < len(pins) {
			index = pins[row]
		}
		if index < len(feed.lines) {
			lines[row], v.feedSnippets[row] = feed.lines[index], feed.snippets[index]
			v.feedQuestions[row] = feed.questions[index]
			if target := feed.snippets[index]; row == v.editHover-1 && target.block == editNavigationSnippet && target == v.snippet {
				lines[row] = activityui.UnderlineEdit(lines[row])
			}
			if row == v.questionHover-1 && feed.questions[index] != 0 {
				lines[row] = underlineLink(lines[row])
			}
		}
	}
	// Pin only when the run keeps a visible line under its heading. Main's
	// transcript items do not always start with a heading, so it never pins.
	if len(feed.running) == 0 && !v.conversation && rows > 1 && v.offset+1 < len(feed.heads) && feed.heads[v.offset] != v.offset && feed.heads[v.offset+1] == feed.heads[v.offset] {
		lines[0], v.feedSnippets[0] = feed.lines[feed.heads[v.offset]], liveActivitySnippet{}
		v.feedQuestions[0] = 0
	}
	// Activity's painter flashes its own entries; Main flashes whole items.
	if target, ok := v.questionRows[v.flashQuestion]; ok && v.conversation && v.now().Before(v.flashUntil) {
		for row := range lines {
			index := v.offset + row - len(pins)
			if row < len(pins) {
				index = pins[row]
			}
			if index < len(feed.heads) && feed.heads[index] == target {
				lines[row] = v.selectRow(lines[row], ansi.StringWidth(lines[row]))
			}
		}
	}
	return lines
}

func (v *liveActivityView) expireFlash(now time.Time) bool {
	if v.flashQuestion == 0 || now.Before(v.flashUntil) {
		return false
	}
	v.flashQuestion, v.flashUntil = 0, time.Time{}
	return true
}

// standalone reports a new event that settles its agent's earlier output: a
// separate operation, message or thinking, not roster-only wait progress.
func (v *liveActivityView) standalone(entry activityPaneEntry) bool {
	return !(entry.native != nil && entry.native.wait != nil) && !(entry.Kind == "tool" && entry.Text == "")
}

// settleActivity collapses successful command output once its agent's
// next standalone event has been followed by a pause. Entries are shared
// between views, so each view replaces rather than modifies native state.
func settleActivity(now time.Time, views ...*liveActivityView) bool {
	// Eligible output shares one deadline, including late completions and
	// other agents. Never fold an earlier result while its batch is settling.
	var outputAt time.Time
	for _, v := range views {
		for _, entry := range v.entries {
			if entry.native == nil || entry.native.settled.IsZero() {
				continue
			}
			next := v.events[entry.Agent]
			if next.seq > entry.Seq {
				outputAt = maxTime(outputAt, maxTime(next.at, entry.native.settled).Add(activityui.OutputDebounce))
			}
		}
	}
	anyChanged := false
	for _, v := range views {
		changed := false
		for i, entry := range v.entries {
			if entry.native == nil {
				continue
			}
			// A later row by the same agent settles the output, a pause after the
			// latest of it and the output's own completion.
			next := v.events[entry.Agent]
			settled := entry.native.settled
			output := !settled.IsZero() && next.seq > entry.Seq && !now.Before(outputAt)
			if !output {
				continue
			}
			v.mutateEntry(i, func(record *liveActivityRecord) {
				native := *record.native
				native.settled, native.collapsed = time.Time{}, true
				record.native = &native
				for j := range record.blocks {
					if block := &record.blocks[j]; block.Collapsible() {
						block.Collapsed, changed = true, true
					}
				}
			})
		}
		anyChanged = changed || anyChanged
	}
	return anyChanged
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (v *liveActivityView) footer(width int) string {
	mode, toggle := "ALL", "a only"
	if v.only {
		mode, toggle = "ONLY", "a all"
	}
	keys := "n/p agent · " + toggle + " · j/k scroll · End bottom"
	if !v.feedOnly {
		keys = "click agent · " + keys
	}

	if width < 60 {
		keys = "n/p · a · j/k · End"
	}
	return ansi.Truncate("\x1b[1m"+mode+activityui.Undim+activityui.Dim+" · "+keys+activityui.Undim, width, "…")
}

func liveActivityAge(age time.Duration) string {
	age = max(0, age.Truncate(time.Second))
	formatted := age.String()
	if age >= time.Minute && age%time.Minute == 0 {
		formatted = strings.TrimSuffix(formatted, "0s")
		if age%time.Hour == 0 {
			formatted = strings.TrimSuffix(formatted, "0m")
		}
	}
	return formatted
}

// liveActivityMiddle shortens a path while keeping its leaf name visible.
func liveActivityMiddle(name string, width int) string {
	if ansi.StringWidth(name) <= width {
		return name + strings.Repeat(" ", width-ansi.StringWidth(name))
	}
	leaf := name[strings.LastIndex(name, "/")+1:]
	if ansi.StringWidth(leaf)+2 >= width {
		return ansi.Truncate(name, width, "…")
	}
	head := ansi.Truncate(name, width-ansi.StringWidth(leaf)-1, "")
	return head + "…" + leaf
}

// showAgent keeps the original roster navigation: all agents precedes the list.
func (v *liveActivityView) showAgent(step int) {
	rows := v.feedAgents()
	if len(rows) == 0 {
		return
	}
	index := -1
	if v.only {
		index = slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
	}
	index = max(-1, min(index+step, len(rows)-1))
	v.hovered = ""
	v.rosterManual = false
	if index < 0 {
		v.only = false
	} else {
		v.selected, v.only = rows[index].agent.Name, true
	}
	v.follow()
}

func (v *liveActivityView) handleRosterKey(escape string, key byte) (string, bool) {
	if key == 27 {
		return "\x1b", false
	}
	if escape != "" {
		escape += string(key)
		if escape == "\x1b[" || escape == "\x1bO" {
			return escape, false
		}
		switch escape {
		case "\x1b[A", "\x1bOA":
			key = 'k'
		case "\x1b[B", "\x1bOB":
			key = 'j'
		default:
			return "", false
		}
	}
	switch key {
	case 3, 'q':
		return "", true
	case 'j', 'n', '\t':
		v.showAgent(1)
	case 'k', 'p':
		v.showAgent(-1)
	case 'a':
		v.only = !v.only
		v.follow()
	}
	return "", false
}
