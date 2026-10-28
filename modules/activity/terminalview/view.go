package terminalview

// Source: apps/mekugi/internal/router/live_activity_view.go:28:1901@[29de53cc].

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/pathdisplay"

	"github.com/yusing/mekugi/contracts/observation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	terminalui "github.com/yusing/mekugi/libraries/terminalui"
)

const (
	// Panes at least this wide place agent cards beside the feed.
	liveActivitySideColumns = 100
	// Operations one live invocation reports together, such as a shell
	// call's Skill, Read and Search, appear this far apart.
	PaceStep = 80 * time.Millisecond
)

// Main and child transcripts share this renderer. Entries carry
// sequence numbers, so a reconnect snapshot merges without duplicating retained rows.
type View struct {
	ImageProtocol                   terminalui.ImageProtocol
	ImageCellWidth, ImageCellHeight int
	ImagePreviews                   map[terminalui.ImageSource]*terminalui.ImagePreview
	ImageDemands                    []terminalui.ImageSource
	ImagePlacements                 []terminalui.ImagePlacement
	ChildReplySummary               bool // Invocation-selected default disclosure for child replies.

	Clock           func() time.Time // Shared with the owning replay UI, nil for live time.
	publication     *activitypresentation.Publication
	entries         []activitypresentation.Entry
	roster          []activitypresentation.AgentRow
	lastSeq         uint64
	Display         Display
	MainStatus      string
	MainSummary     []Block
	DisplayRequest  *DisplayIntent
	outputVersions  map[outputIdentity]int
	outputCache     map[activitypresentation.OutputDemand]activitypresentation.OutputResult
	outputDemands   []activitypresentation.OutputDemand
	completed       map[uint64]time.Time
	commandFrames   map[uint64]*heldCommandFrame
	reasoningFrames map[observation.ItemRef]*reasoningFrame
	collapsed       map[uint64]bool
	Disclosure      Disclosure
	Selected        string
	Hovered         string
	Hits            []Hit
	Following       bool
	Offset          int
	RosterOffset    int
	RosterManual    bool
	RosterSelected  string
	rosterEnd       int
	SkillsHover     string // Agent whose loaded-skill count is under the pointer.
	SkillsRequest   string // Agent whose active skills a roster click asked to open.
	Unseen          int
	StatusLabel     string
	RoleColors      map[string]string
	LivePreviews    map[string][]string // Temporary caller-local transcript replacements.
	FeedOnly        bool
	Conversation    bool                        // Full, unclipped messages in the displayed transcript.
	ChildrenOnly    bool                        // Main owns root activity; this view retains child entries.
	Bare            bool                        // The shell's pane title replaces the heading and footer rows.
	Focused         bool                        // Show key hints only while the view has keyboard focus.
	LineCounts      map[string]diffmodel.Counts // Captured edit lines by caller key, for the native roster.
	NetCounts       *diffmodel.Counts           // Composed project outcome, independent of roster rows.
	rosterPace      map[string]rosterMetricPace // Roster metrics easing toward their latest values, by agent.
	RosterLines     map[string]diffmodel.Counts // Line counts as last shown by the roster.
	RosterEasing    bool                        // The last roster frame showed metrics still easing.
	Painter         Painter
	Runs            map[RunKey]Run
	runGeneration   uint64
	syntaxWindow    *liveActivitySyntaxWindow // Invocation-local rows being decorated within a run.
	Paced           map[uint64]PaceState      // Live invocations still revealing their operations, by entry.
	Events          map[string]Event          // Each agent's latest standalone event, which settles its output.
	pacedSeq        uint64                    // Entries up to this sequence have been considered for pacing.

	conversationItems   []conversationItem
	conversationOrdered []conversationItem
	conversationHeld    []conversationItem

	Expansion       uint8 // 0 default, 1 expanded events, 2 expanded all.
	FeedSpans       []Span
	ExpansionAnchor *Span // Top item and its local row before a toggle.

	// opening names the Snippet requested in the shared dialog.
	Snippet Snippet // Hovered content target.
	Opening Snippet
	// Passed holds Main's sent messages and operation batches that have
	// scrolled above the viewport. Messages then show as excerpts linking to
	// Activity, and batches as one row opening their operations.
	Passed map[uint64]bool

	// Geometry of the last frame, used by scrolling keys and the pointer.
	// feedSnippets holds each feed row's snippet, from screen row feedTop
	// between columns feedLeft and feedRight.
	FeedLines, FeedRows int
	FeedSnippets        []Snippet
	FeedQuestions       []uint64
	CopyRows            [][]terminalui.CopySpan
	QuestionRows        map[uint64]int
	PendingTarget       uint64 // Cross-pane jump resolved after the destination layout is rendered.
	// QuestionHover is the pointed viewport row plus one; zero points at none.
	QuestionHover                        int
	EditHover                            int // Pointed viewport row plus one, not the whole capture.
	FlashQuestion                        uint64
	FlashUntil                           time.Time
	FeedTop, FeedLeft, FeedRight         int
	RosterTop, RosterBottom, RosterRight int
	Width, Height                        int
}

// Snippet names a clippable block in the shared feed: the
// sequence of its run's first entry and its index in the run. Sequences start
// at one, so the zero value names no snippet.
// EditNavigationSnippet routes a compact edit row to its captured change.
const EditNavigationSnippet = -1

type Snippet struct {
	Run   uint64
	Block int
	Path  string // Exact edit-row target, before display shortening.
}

type Run struct {
	generation uint64 // Last layout pass that used this exact run.
	Colored    bool   // Syntax decoration has been materialized.
	painted    []bool // Decorated rows in a partially visible run.
	Lines      []string
	Blocks     []Block        // Laid-out blocks, indexed by the snippets naming them.
	Snippets   []Snippet      // Aligned with lines.
	Questions  []uint64       // Clickable question targets, aligned with lines.
	EntryRows  map[uint64]int // Exact Activity entry starts within a grouped run.
	Batch      bool           // Main operations that fold into one row once out of view.
}

// Event is an agent's latest standalone entry and when it arrived.
type Event struct {
	Seq uint64
	At  time.Time
}

// PaceState reveals a live invocation's operations one at a time.
type PaceState struct {
	Shown int
	Next  time.Time
}

type Hit struct {
	Row, First, Last int // One-based terminal coordinates, inclusive.
	Agent            string
	Skills           bool // Opens the agent's active skills rather than selecting it.
}

type RunKey struct {
	First, Last uint64
	Revision    uint64
	Width, Clip int
	Theme       diffrender.Theme
	Hover       int // Hovered snippet block in this run, or -1.
	Main        bool
	Thread      ConversationThread // Main transcript thread placement.
	Lead        uint64             // Main item a transcript tool group continues, or 0.
	Flash       uint64             // Flashed Activity entry in this run, or 0.
	Excerpt     bool               // Main sent message or batch shortened out of view.
	Tail        int                // Tail lines open output shows; 0 shows the whole tail.
	Expansion   uint8
}

// trackPace starts pacing new live invocations that report several
// operations at once. Restored history and in-place updates show at once.
func (v *View) trackPace(now time.Time) {
	for i := len(v.entries) - 1; i >= 0 && v.entries[i].Seq > v.pacedSeq; i-- {
		entry := v.entries[i]
		if entry.Seq > v.lastSeq || entry.Kind != "tool" || !entry.Live || len(v.entries[i].Values) < 2 {
			continue
		}
		if v.Paced == nil {
			v.Paced = make(map[uint64]PaceState)
		}
		v.Paced[entry.Seq] = PaceState{Shown: 1, Next: now.Add(PaceStep)}
	}
	v.pacedSeq = max(v.pacedSeq, v.lastSeq)
}

// Pace reveals the next operation of each paced invocation that is due.
func (v *View) Pace(now time.Time) bool {
	changed := false
	for seq, pace := range v.Paced {
		i := slices.IndexFunc(v.entries, func(entry activitypresentation.Entry) bool { return entry.Seq == seq })
		if i >= 0 && now.Before(pace.Next) {
			continue
		}
		// A late frame catches up on every step that fell due.
		for i >= 0 && pace.Shown < len(v.entries[i].Values) && !now.Before(pace.Next) {
			pace.Shown, pace.Next, changed = pace.Shown+1, pace.Next.Add(PaceStep), true
			v.InvalidateRuns(seq)
		}
		if i < 0 || pace.Shown >= len(v.entries[i].Values) {
			delete(v.Paced, seq)
		} else {
			v.Paced[seq] = pace
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
func (v *View) tailRows() int {
	if v.Height > 0 && v.Height < liveActivityCompactHeight {
		return liveActivityCompactTail
	}
	return 0
}

func (v *View) shownBlocks(i int) []Block {
	if pace, ok := v.Paced[v.entries[i].Seq]; ok && pace.Shown < len(v.entries[i].Values) {
		return v.blocks(i)[:pace.Shown]
	}
	return v.blocks(i)
}

// KeepSelection falls back to the first agent when the selection is unknown.
func (v *View) KeepSelection() {
	rows := v.feedAgents()
	if v.Selected == "" || !slices.ContainsFunc(rows, func(row activitypresentation.AgentRow) bool { return row.Agent.Name == v.Selected }) {
		v.Selected = ""
		if len(rows) > 0 {
			v.Selected = rows[0].Agent.Name
		}
	}
}

// feedAgents are the roster rows the feed can filter to. Native Activity
// never shows Main's entries, so it neither counts nor selects Main.
func (v *View) feedAgents() []activitypresentation.AgentRow {
	rows := v.Roster()
	if v.Display.SideCaller != "" {
		rows = append([]activitypresentation.AgentRow{{Agent: activitypresentation.Agent{AgentFacts: activitypresentation.AgentFacts{Name: v.Display.SideCaller}}}}, rows...)
	}
	rows = slices.DeleteFunc(rows, func(row activitypresentation.AgentRow) bool {
		return row.Agent.NonAttachable || v.ChildrenOnly && row.Agent.Name == "/root"
	})
	return rows
}

func (v *View) Visible(entry activitypresentation.Entry) bool {
	if entry.Hidden() {
		return false
	}

	if v.Expansion == 0 && entry.Kind == "hook" { // Expanded presentations disclose hook runs.
		return false
	}
	if entry.Kind == "reasoning" {
		if v.Conversation {
			return entry.Agent == v.conversationSpeaker()
		}
		if !v.ChildrenOnly {
			return false
		}
	}
	if v.ChildrenOnly && entry.Agent == "/root" {
		return false
	}
	return !v.displayingChild() || entry.Agent == v.displayedCaller()
}

// Glyph shows only observed facts: an open provider response, a latest error
// event, or a sent plaintext final answer. None of them claims completion.
func (v *View) Glyph(agent activitypresentation.Agent) string {
	if role := Role(agent); role != "" {
		return v.RoleColor(role) + string(v.agentStatus(agent)) + terminalui.Reset
	}
	switch v.agentStatus(agent) {
	case '◐':
		return terminalui.Amber + "◐" + terminalui.Reset
	case '!':
		return terminalui.Red + "!" + terminalui.Reset
	case '✓':
		return terminalui.Green + "✓" + terminalui.Reset
	}
	return terminalui.Dim + "·" + terminalui.Undim
}

// agentStatus is the glyph's fact; counts use it so they agree with visible rows.
func (v *View) agentStatus(agent activitypresentation.Agent) rune {
	switch agent.Status {
	case "responding", "waiting":
		return '◐'
	case "error":
		return '!'
	case "answered":
		return '✓'
	}
	return '·'
}

// StatusCounts counts responding and error glyphs among rows.
func (v *View) StatusCounts(rows []activitypresentation.AgentRow) (responding, errors int) {
	for _, row := range rows {
		switch v.agentStatus(row.Agent) {
		case '◐':
			responding++
		case '!':
			errors++
		}
	}
	return responding, errors
}

func (v *View) HandleMouse(action byte, row, column int) bool {
	if action == 'j' || action == 'k' {
		v.QuestionHover = 0 // Scrolling moves the link from under the pointer.
		v.EditHover = 0
		if !v.FeedOnly && row >= v.RosterTop && row <= v.RosterBottom && column >= 1 && column <= v.RosterRight {
			if v.RosterTop == v.RosterBottom {
				return true
			}
			step := 1
			if action == 'k' {
				step = -1
			}
			v.ScrollRoster(step)
			return true
		}
		return v.ScrollKey(terminalui.PaneWheelKey(action))
	}

	if action != 'h' && action != '\r' {
		return false
	}
	if action == '\r' && column >= v.FeedLeft && column <= v.FeedRight {
		index := row - v.FeedTop
		if index >= 0 && index < len(v.FeedQuestions) && v.FeedQuestions[index] != 0 {
			if target, ok := v.QuestionRows[v.FeedQuestions[index]]; ok {
				v.Offset, v.Following = target, false
				v.FlashQuestion = v.FeedQuestions[index]
				v.FlashUntil = v.Now().Add(700 * time.Millisecond)
				return true
			}
		}
	}
	link := v.pointQuestion(row, column)
	snippet := v.PointSnippet(action, row, column)
	agent := false
	if !v.FeedOnly {
		agent = v.PointAgent(action, row, column)
	}
	return link || snippet || agent
}

// pointQuestion underlines the question link under the pointer.
func (v *View) pointQuestion(row, column int) bool {
	hover := 0
	if index := row - v.FeedTop; index >= 0 && index < len(v.FeedQuestions) && v.FeedQuestions[index] != 0 && column >= v.FeedLeft && column <= v.FeedRight {
		hover = index + 1
	}
	previous := v.QuestionHover
	v.QuestionHover = hover
	return hover != previous
}

// underlineLink underlines a question link from its arrow on, leaving the
// gutter plain.
func underlineLink(line string) string {
	lead, link, ok := strings.Cut(line, "↩")
	if !ok {
		return line
	}
	return lead + "\x1b[4m" + strings.ReplaceAll("↩"+link, terminalui.Reset, terminalui.Reset+"\x1b[4m") + "\x1b[24m"
}

// PointSnippet underlines a hovered snippet's count. A click on an operation
// asks to open its full content in the shared dialog.
func (v *View) PointSnippet(action byte, row, column int) bool {
	var snippet Snippet
	editHover := 0
	if index := row - v.FeedTop; index >= 0 && index < len(v.FeedSnippets) && column >= v.FeedLeft && column <= v.FeedRight {
		snippet = v.FeedSnippets[index]
		if snippet.Block == EditNavigationSnippet {
			editHover = index + 1
		}
	}
	redraw := editHover != v.EditHover
	v.EditHover = editHover
	if action == '\r' && snippet != (Snippet{}) {
		v.Opening = snippet
		redraw = true
	}
	if snippet != v.Snippet {
		v.Snippet, redraw = snippet, true
	}
	return redraw
}

// ClickTarget prepares a block whose rows a click acts on, and reports whether
// they do: operations, compact reasoning and collapsed narrative text open the shared dialog.
// Under the pointer, the block underlines its count.
func (v *View) ClickTarget(block *Block, snippet Snippet, width int) bool {
	if block.Kind == "compaction" || block.DetailCode != "" || len(block.WaitTargets) > 2 {
		block.Hovered = v.Snippet == snippet
		return true
	}
	if block.Kind == "summary" || block.Kind == "error" {
		details := block.Kind == "summary" && v.Painter.ReasoningElided(*block, width) || block.Kind == "error" && ErrorHasDetails(*block)
		block.Hovered = details && v.Snippet == snippet
		return details
	}
	if OutputBlock(*block) || block.Kind == "progress" && block.Label != "" && strings.TrimSpace(block.Body) != "" {
		block.Hovered = v.Snippet == snippet
		return true
	}
	if !block.Collapsed {
		return false
	}
	block.Hovered = block.Collapsed && v.Snippet == snippet
	return true
}

// OutputBlock reports an operation the output dialog opens: one with output,
// still running, carrying source a row may clip, or standing for several
// reads. Edits open their captured change instead, and questions their dock.
func OutputBlock(block Block) bool {
	if block.DetailCode != "" {
		return true
	}
	if block.Kind != "op" && block.Kind != "reads" || block.EditOperation() || len(block.Questions) > 0 {
		return false
	}
	return block.Output != 0 || len(block.Tail)+block.TailOmitted > 0 || len(block.Changes) > 0 || block.Code != "" || len(block.Members) > 0
}

// SnippetBlock is the block a snippet names in the last frame.
func (v *View) SnippetBlock(snippet Snippet) (Block, bool) {
	for key, run := range v.Runs {
		if key.Expansion == v.Expansion && key.First == snippet.Run && snippet.Block >= 0 && snippet.Block < len(run.Blocks) {
			return run.Blocks[snippet.Block], true
		}
	}
	return Block{}, false
}

// PointAgent highlights a hovered roster agent; a click filters the feed.
func (v *View) PointAgent(action byte, row, column int) bool {
	previous, previousSkills := v.Hovered, v.SkillsHover
	v.Hovered, v.SkillsHover = "", ""
	for _, hit := range v.Hits {
		if hit.Row == row && column >= hit.First && column <= hit.Last {
			if slices.ContainsFunc(v.roster, func(row activitypresentation.AgentRow) bool {
				return row.Agent.Name == hit.Agent && row.Agent.NonAttachable
			}) {
				return previous != "" || previousSkills != ""
			}
			if hit.Skills {
				v.SkillsHover = hit.Agent
			} else {
				v.Hovered = hit.Agent
			}
			if action == '\r' && hit.Skills {
				v.SkillsRequest, v.SkillsHover = hit.Agent, ""
				return true
			}
			if action == '\r' {
				if v.ChildrenOnly && hit.Agent == "/root" || !v.ChildrenOnly && v.displayingChild() && v.displayedCaller() == hit.Agent {
					v.requestDisplay("/root")
				} else {
					v.requestDisplay(hit.Agent)
				}
				v.RosterManual = false
				v.Hovered = ""
				v.Follow()
			}
			return action == '\r' || previous != v.Hovered || previousSkills != v.SkillsHover
		}
	}
	return previous != "" || previousSkills != ""
}

func (v *View) Follow() {
	v.Following, v.Unseen = true, 0
}

func (v *View) ScrollKey(key byte) bool {
	offset := v.Offset
	if v.Following {
		offset = max(0, v.FeedLines-v.FeedRows)
	}
	next, follow, ok := terminalui.PaneScroll(key, offset, v.FeedRows, v.FeedLines)
	if ok {
		v.Offset = max(0, min(next, v.FeedLines-v.FeedRows))
		v.Following = follow || v.Offset == max(0, v.FeedLines-v.FeedRows)
		if v.Following {
			v.Unseen = 0
		}
	}
	return ok
}

// Render lays the pane out for its size: agent cards beside the feed on wide
// panes, a roster above the feed on narrower ones, and a one-line strip when
// rows are scarce. Every row fits within width-1 columns.
func (v *View) Render(width, height int, now time.Time) []string {
	width = max(10, width)
	if v.Conversation {
		height = max(1, height)
	} else {
		height = max(3, height)
	}
	if width != v.Width || height != v.Height {
		v.Hovered, v.Snippet, v.QuestionHover = "", Snippet{}, 0
		v.Width, v.Height = width, height
	}
	v.Hits = v.Hits[:0]
	v.FeedSnippets = nil
	v.FeedQuestions = nil
	v.RosterTop, v.RosterBottom, v.RosterRight = 0, 0, 0
	text := max(1, width-1)
	rows := v.Roster()
	footer := height >= 10 && !v.Conversation && (!v.ChildrenOnly || v.Focused)
	body := height - 1
	if footer {
		body--
	}
	lines := []string{v.Header(rows, text)}
	v.FeedTop, v.FeedLeft, v.FeedRight = 2, 1, text
	if v.Conversation || v.Bare {
		// Main's composer border or the pane title carries the state; the
		// feed takes every row.
		body, lines, v.FeedTop, footer = height, nil, 1, false
	}
	switch {
	case v.FeedOnly:
		hint := !v.Following && body > 1
		feedRows := body
		if hint {
			feedRows--
		}
		feed := v.RenderFeed(text, feedRows)
		lines = append(lines, v.Viewport(feed, feedRows)...)
		if hint {
			label := "↓ Back to bottom · esc"
			lines = append(lines, liveActivityHint(v.Painter.Theme, label, text))
		}
	case len(rows) > 0 && text >= liveActivitySideColumns && body >= 6:
		cardWidth := min(44, max(28, text*3/10))
		feedWidth := text - cardWidth - 3
		cards := v.RenderCards(rows, cardWidth, body, now)
		v.RosterTop, v.RosterBottom, v.RosterRight = 2, body+1, cardWidth
		feed := v.Viewport(v.RenderFeed(feedWidth, body), body)
		v.FeedLeft = cardWidth + 4
		for i := range body {
			lines = append(lines, liveActivityPad(cards[i], cardWidth)+terminalui.Dim+" │ "+terminalui.Undim+feed[i])
		}
	case len(rows) > 0 && body >= 8:
		roster := v.RenderRoster(rows, text, max(2, body/3), now)
		v.RosterTop, v.RosterBottom, v.RosterRight = 2, len(roster)+1, text
		feedRows := body - len(roster) - 1
		lines = append(lines, roster...)
		lines = append(lines, terminalui.Dim+strings.Repeat("─", text)+terminalui.Undim)
		v.FeedTop = len(lines) + 1
		lines = append(lines, v.Viewport(v.RenderFeed(text, feedRows), feedRows)...)
	case len(rows) > 0:
		v.RosterTop, v.RosterBottom, v.RosterRight = 2, 2, text
		lines = append(lines, v.RenderStrip(rows, text))
		v.FeedTop = 3
		lines = append(lines, v.Viewport(v.RenderFeed(text, body-1), body-1)...)
	default:
		lines = append(lines, v.Viewport(v.RenderFeed(text, body), body)...)
	}
	if footer {
		lines = append(lines, v.Footer(text))
	}
	v.CopyRows = make([][]terminalui.CopySpan, len(lines))
	for row := range lines {
		lines[row], v.CopyRows[row] = terminalui.ExtractCopy(lines[row])
	}
	return lines
}

func liveActivityHint(theme diffrender.Theme, label string, width int) string {
	label = ansi.Truncate(label, width, "")
	return strings.Repeat(" ", max(0, (width-ansi.StringWidth(label))/2)) + theme.Accent() + label + terminalui.Reset
}

func liveActivityPad(line string, width int) string {
	line = ansi.Truncate(line, width, "…")
	return line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))
}

func (v *View) Header(rows []activitypresentation.AgentRow, width int) string {
	return v.RenderHeader(rows, width, false)
}

func (v *View) RenderHeader(rows []activitypresentation.AgentRow, width int, rosterOnly bool) string {
	title := "AGENTS"
	if v.FeedOnly && !rosterOnly {
		title = "ACTIVITY"
	}
	if v.Conversation && !rosterOnly {
		title = "MAIN"
	}

	if v.ChildrenOnly && !rosterOnly {
		return v.activityHeader(width)
	}
	left := "\x1b[1m" + v.Painter.Theme.Accent() + title + terminalui.Reset
	responding, errors := v.StatusCounts(rows)
	switch {
	case v.FeedOnly && !rosterOnly && !v.displayingChild():
		left += " · all"
	case len(rows) == 0:
		left += terminalui.Dim + " · waiting for subagent activity" + terminalui.Undim
	case v.displayingChild() && !rosterOnly:
		index := slices.IndexFunc(rows, func(row activitypresentation.AgentRow) bool { return row.Agent.Name == v.displayedCaller() })
		left += fmt.Sprintf(" · only %s %s(%d/%d)%s", v.Painter.Agent(v.displayedCaller()), terminalui.Dim, index+1, len(rows), terminalui.Undim)
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
	if !v.Following {
		right = terminalui.Amber + "PAUSED" + terminalui.Reset
		if v.Unseen > 0 {
			right += fmt.Sprintf(" · %d new", v.Unseen)
		}
	}

	if v.StatusLabel != "" {
		right = v.StatusLabel
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
func (v *View) activityHeader(width int) string {
	left := "\x1b[1m" + v.Painter.Theme.Accent() + "Activity" + terminalui.Reset
	if v.displayingChild() {
		rows := v.feedAgents()
		index := slices.IndexFunc(rows, func(row activitypresentation.AgentRow) bool { return row.Agent.Name == v.displayedCaller() })
		left += terminalui.Dim + " · only " + terminalui.Undim + v.Painter.Agent(v.displayedCaller()) + terminalui.Dim + fmt.Sprintf(" %d/%d", index+1, len(rows)) + terminalui.Undim
	}
	right := v.StatusLabel
	if right == "" && !v.Following {
		right = terminalui.Amber + "paused" + terminalui.Reset
		if v.Unseen > 0 {
			right += terminalui.Amber + fmt.Sprintf(" · %d new", v.Unseen) + terminalui.Reset
		}
		right += terminalui.Dim + " · End follows" + terminalui.Undim
	}
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if right == "" || gap < 2 {
		return ansi.Truncate(left, width, "…")
	}
	return left + strings.Repeat(" ", gap) + right
}

// Current is an agent's latest activity summary and its elapsed/response timer.
func (v *View) Current(agent activitypresentation.Agent, now time.Time, widths ...int) (string, string) {
	width := 80
	if len(widths) > 0 {
		width = max(1, widths[0])
	}
	summary := terminalui.Dim + "—" + terminalui.Undim
	if agent.Name == "/root" && v.MainStatus != "" {
		summary = v.MainStatus
	}
	blocks := v.SummaryBlocks(agent.Summary)
	if agent.Name == "/root" && v.MainSummary != nil {
		blocks = slices.Clone(v.MainSummary)
	}
	if len(blocks) > 0 {
		blocks[len(blocks)-1].Live = agent.Responding && blocks[len(blocks)-1].Live
		// Keep syntax cache ownership on the view, including its byte accounting.
		clock := v.Painter.Clock
		v.Painter.Clock = func() time.Time { return now }
		summary = v.Painter.Summary(blocks, width)
		v.Painter.Clock = clock
	}
	return summary, agentTimer(agent, now)
}

func agentTimer(agent activitypresentation.Agent, now time.Time) string {
	last := cmp.Or(agent.LastResponse, agent.Started)
	if agent.Started.IsZero() || !agent.WorkKnown {
		return ""
	}
	elapsed := agent.WorkElapsed
	duration := FormatDuration(elapsed)
	if !agent.WorkSince.IsZero() {
		elapsed += max(0, now.Sub(agent.WorkSince))
		duration = FormatLiveDuration(elapsed)
	}
	if duration == "" {
		return ""
	}
	return duration + " · " + Last(last, now)
}

// SummaryBlocks applies this feed's disclosure and pacing before the application
// joins Main's detached summary into the child roster.
func (v *View) SummaryBlocks(entries []activitypresentation.Entry) []Block {
	var blocks []Block
	var last activitypresentation.Entry
	for i, entry := range entries {
		entry = v.commandEntry(entry)
		entry = v.reasoningEntry(entry)
		last = entry
		if entry.Suppressed {
			continue
		}
		next := v.Disclosure.Blocks(entry.Values)
		if pace, ok := v.Paced[entry.Seq]; ok && pace.Shown < len(next) {
			next = next[:pace.Shown]
		}
		if i < len(entries)-1 {
			slices.Reverse(next)
		}
		blocks = append(blocks, next...)
	}
	if len(blocks) > 0 {
		blocks[len(blocks)-1].Live = last.StatusAnimating()
	}
	return blocks
}

// Tokens shows cumulative input (sent) and output (received) tokens.
func Tokens(agent activitypresentation.Agent) string {
	if !agent.TokensKnown && agent.InputTokens == 0 && agent.OutputTokens == 0 {
		return ""
	}
	input, output := tokenParts(agent)
	return "↑ " + input + " ↓ " + output
}

func tokenParts(agent activitypresentation.Agent) (string, string) {
	prefix := ""
	if agent.UsagePartial {
		prefix = "≥"
	}
	return prefix + FormatUsageTokens(agent.InputTokens), prefix + FormatUsageTokens(agent.OutputTokens)
}

// Cost omits unknown cost rather than claiming zero; a response
// that ended without usage makes the observed cost a lower bound.
func Cost(agent activitypresentation.Agent) string {
	if agent.Turns == 0 || !agent.CostKnown {
		return ""
	}
	if agent.CostPartial {
		return fmt.Sprintf("≥$%.2f", agent.Cost)
	}
	return fmt.Sprintf("$%.2f", agent.Cost)
}

// Turns counts provider roundtrips as T+N.
func Turns(agent activitypresentation.Agent) string {
	if agent.Roundtrips == 0 {
		return ""
	}
	prefix := ""
	if agent.RoundtripsPartial {
		prefix = "≥"
	}
	return terminalui.Dim + prefix + "T+" + terminalui.Undim + fmt.Sprint(agent.Roundtrips)
}

// selectRow fills the selected agent's rows, so selection takes no column of
// its own. Resets inside the row restore the fill.
func (v *View) selectRow(line string, width int) string {
	fill := v.Painter.Theme.SelectionBackground()
	line = strings.ReplaceAll(line, terminalui.Reset, terminalui.Reset+fill)
	return fill + line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line))) + "\x1b[49m"
}

func (v *View) hoverName(name, agent string) string {
	if agent == v.Hovered {
		return Underline(name)
	}
	return name
}

// RenderCards gives the name, full-width activity, and short metrics separate rows.
func (v *View) RenderCards(rows []activitypresentation.AgentRow, width, height int, now time.Time) []string {
	lines := v.renderAgentRows(rows, width, height, now, true)
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func (v *View) RenderRoster(rows []activitypresentation.AgentRow, width, limit int, now time.Time) []string {
	return v.renderAgentRows(rows, width, limit, now, false)
}

func (v *View) ScrollRoster(step int) {
	if step > 0 && v.RosterManual && v.rosterEnd >= len(v.roster) {
		return
	}
	v.RosterOffset = max(0, min(v.RosterOffset+step, len(v.roster)-1))
	v.RosterManual, v.Hovered = true, ""
}

// RosterRange gives compact agent rows priority over secondary metrics.
// Overflow indicators consume space only when there are hidden agents.
func RosterRange(count, start, selected, limit int) (end int, metrics bool) {
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

// RosterTree draws guides only from ancestors that are drawn as branches, so
// a row whose parent is off-screen shows its relative path without guides.
func RosterTree(rows []activitypresentation.AgentRow, index, start int) (name, indent string) {
	parentAt := func(i int) int {
		parent, _, _ := strings.CutLast(rows[i].Agent.Name, "/")
		for j := i - 1; j >= start; j-- {
			if rows[j].Agent.Name == parent {
				return j
			}
		}
		return -1
	}
	continuation := func(i int) bool {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].Depth < rows[i].Depth {
				break
			}
			if rows[j].Depth == rows[i].Depth {
				return true
			}
		}
		return false
	}
	parent := parentAt(index)
	if rows[index].Depth == 0 || parent < 0 {
		return pathdisplay.AgentDisplayName(rows[index].Agent.Name), ""
	}
	var guides []string
	for i := parent; i >= 0; i = parentAt(i) {
		if rows[i].Depth == 0 || parentAt(i) < 0 {
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
	_, leaf, _ := strings.CutLast(rows[index].Agent.Name, "/")
	return prefix + branch + leaf, prefix + guide
}

func (v *View) hiddenRoster(rows []activitypresentation.AgentRow, direction string) string {
	responding, errors := v.StatusCounts(rows)
	line := fmt.Sprintf("%s %d more", direction, len(rows))
	if responding > 0 {
		line += fmt.Sprintf(" · %d ◐", responding)
	}
	if errors > 0 {
		line += fmt.Sprintf(" · %d !", errors)
	}
	return terminalui.Dim + line + terminalui.Undim
}

// renderAgentRows gives each roster agent one row with its metrics inline;
// cards give the name, activity, and short metrics separate rows.
func (v *View) renderAgentRows(rows []activitypresentation.AgentRow, width, limit int, now time.Time, cards bool) []string {
	if limit <= 0 || len(rows) == 0 {
		return nil
	}
	selected := max(0, slices.IndexFunc(rows, func(row activitypresentation.AgentRow) bool { return row.Agent.Name == v.Selected }))
	stride := 1
	if cards {
		stride = 3
	}
	compact := len(rows)*stride > limit
	start, end, metrics := 0, len(rows), false
	if compact {
		start = min(v.RosterOffset, len(rows)-1)
		if v.RosterSelected != v.Selected {
			v.RosterManual = false
		}
		if !v.RosterManual {
			start = min(start, selected)
		}
		end, metrics = RosterRange(len(rows), start, selected, limit)
		for !v.RosterManual && selected >= end && start < selected {
			start++
			end, metrics = RosterRange(len(rows), start, selected, limit)
		}
	}
	v.RosterOffset, v.rosterEnd, v.RosterSelected = start, end, v.Selected
	names, indents := make([]string, end-start), make([]string, end-start)
	nameWidth := 1
	for i := start; i < end; i++ {
		name, indent := RosterTree(rows, i, start)
		names[i-start], indents[i-start] = name, indent
		nameWidth = max(nameWidth, ansi.StringWidth(name))
	}
	nameWidth = min(nameWidth, max(1, width/3))
	var table []string
	if !cards {
		table = v.MetricTable(rows[start:end], now)
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
		name = strings.TrimRight(Middle(name, available), " ")
		split := strings.LastIndexAny(name, " /") + 1
		styled := terminalui.DimAgentColor(row.Agent.Name) + name[:split] + terminalui.Undim + terminalui.AgentColor(row.Agent.Name) + v.hoverName(name[split:], row.Agent.Name) + terminalui.Reset
		prefix := " " + v.Glyph(row.Agent) + " "
		switch {
		case cards && !compact:
			last := Last(cmp.Or(row.Agent.LastResponse, row.Agent.Started), now)
			gap := width - 3 - ansi.StringWidth(name) - ansi.StringWidth(last)
			line := prefix + styled
			if gap >= 2 {
				line += strings.Repeat(" ", gap) + terminalui.Dim + last + terminalui.Undim
			}
			add(line)
			summary, _ := v.Current(row.Agent, now, width-3-ansi.StringWidth(indents[i-start]))
			add("   " + terminalui.Dim + indents[i-start] + terminalui.Undim + summary)
		case cards:
			line := prefix + styled + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name))) + "  "
			summary, _ := v.Current(row.Agent, now, width-ansi.StringWidth(line))
			add(line + summary)
		default:
			line := prefix + styled + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name))) + "  "
			metric := table[i-start]
			room := width - ansi.StringWidth(line) - 2 - ansi.StringWidth(metric)
			if metric == "" || room < 20 {
				summary, _ := v.Current(row.Agent, now, width-ansi.StringWidth(line))
				add(line + summary)
			} else {
				summary, _ := v.Current(row.Agent, now, room)
				add(line + liveActivityPad(summary, room) + "  " + metric)
			}
		}
		if cards && (!compact || (metrics && i == selected)) {
			add("   " + terminalui.Dim + indents[i-start] + terminalui.Undim + cardMetrics(row.Agent))
		}
		if i == selected {
			for j := first - 2; j < len(lines); j++ {
				lines[j] = v.selectRow(lines[j], width)
			}
		}
		for hit := first; hit < len(lines)+2; hit++ {
			v.Hits = append(v.Hits, Hit{Row: hit, First: 1, Last: width, Agent: row.Agent.Name})
		}
	}
	if end < len(rows) && len(lines) < limit {
		add(v.hiddenRoster(rows[end:], "↓"))
	}
	return lines
}

func Last(last, now time.Time) string {
	if last.IsZero() {
		return "—"
	}
	age := max(0, now.Sub(last))
	if age < 2*time.Second {
		return "just now"
	}
	return FormatDuration(age) + " ago"
}

// MetricTable reserves stable timer, input/output, cost, and turn slots even
// before values arrive. Large values clip rather than shifting adjacent columns.
func (v *View) MetricTable(rows []activitypresentation.AgentRow, now time.Time) []string {
	const columns = 4
	cells := make([][columns]string, len(rows))
	widths := [columns]int{14, 17, 7, 5}
	for i, row := range rows {
		_, timer := v.Current(row.Agent, now)
		tokens := Tokens(row.Agent)
		if tokens != "" {
			input, output := tokenParts(row.Agent)
			tokens = terminalui.Dim + "↑ " + terminalui.Undim + liveActivityPad(input, 6) + terminalui.Dim + " ↓ " + terminalui.Undim + liveActivityPad(output, 6)
		}
		timerCell := ""
		if timer != "" {
			elapsed, age, _ := strings.Cut(timer, " · ")
			timerCell = strings.Repeat(" ", max(0, 3-len(elapsed))) + metricValues(elapsed) + terminalui.Dim + " · " + terminalui.Undim + metricValues(age)
		}
		cost, turns := "", ""
		if row.Agent.Turns > 0 && row.Agent.CostKnown {
			prefix := " "
			if row.Agent.CostPartial {
				prefix = "≥"
			}
			cost = prefix + "$" + fmt.Sprintf("%.2f", row.Agent.Cost)
		}
		turns = Turns(row.Agent)
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

// Role omits the root's redundant role.
func Role(agent activitypresentation.Agent) string {
	if agent.Name == "/root" {
		return ""
	}
	return strings.Join(strings.Fields(textsanitize.Safe(agent.Role, false)), " ")
}

// Keep numerical values bright while labels and units recede, including
// every unit of a compound duration such as 1m34s.
func metricValues(text string) string {
	var b strings.Builder
	dim := false
	for _, r := range text {
		if value := r >= '0' && r <= '9' || r == '.'; value == dim {
			if dim = !value; dim {
				b.WriteString(terminalui.Dim)
			} else {
				b.WriteString(terminalui.Undim)
			}
		}
		b.WriteRune(r)
	}
	if dim {
		b.WriteString(terminalui.Undim)
	}
	return b.String()
}

func cardMetrics(agent activitypresentation.Agent) string {
	var parts []string
	if cost := Cost(agent); cost != "" {
		parts = append(parts, cost)
	} else if agent.InputTokens > 0 {
		parts = append(parts, terminalui.Dim+"↑ "+terminalui.Undim+FormatUsageTokens(agent.InputTokens))
	}
	if turns := Turns(agent); turns != "" {
		parts = append(parts, turns)
	}
	return strings.Join(parts, terminalui.Dim+" · "+terminalui.Undim)
}

// RenderStrip is the one-line roster for short panes.
func (v *View) RenderStrip(rows []activitypresentation.AgentRow, width int) string {
	var parts []string
	column := 1
	for _, row := range rows {
		name := pathdisplay.AgentDisplayName(row.Agent.Name)
		if row.Agent.Name == v.Selected || row.Agent.Name == v.Hovered {
			name = Underline(name)
		}
		part := v.Glyph(row.Agent) + " " + terminalui.AgentColor(row.Agent.Name) + name + terminalui.Reset
		if last := min(width, column+ansi.StringWidth(part)-1); column <= last {
			v.Hits = append(v.Hits, Hit{Row: 2, First: column, Last: last, Agent: row.Agent.Name})
		}
		parts = append(parts, part)
		column += ansi.StringWidth(part) + 2
	}
	return ansi.Truncate(strings.Join(parts, "  "), width, "…")
}

type Feed struct {
	images    []feedImage
	Spans     []Span
	paints    []liveActivityPaint // Cold runs to decorate if they enter the viewport.
	Lines     []string
	Heads     []int     // Index of the heading that owns each line.
	Snippets  []Snippet // Snippet that owns each line, if any.
	Questions []uint64
	Passing   []liveActivityPassing
}

type liveActivityPaint struct {
	start        int
	key          RunKey
	render       func() Run
	conversation conversationPaint
}

type liveActivitySyntaxWindow struct{ first, last int }

// paintBlock measures an independent block without syntax before decorating
// it. Grouped runs can be arbitrarily long; intersection with their heading
// must not tokenize every operation they contain.
func (v *View) paintBlock(start, limit int, render func() []string) []string {
	if v.syntaxWindow == nil {
		return render()
	}
	saved := v.Painter.LayoutOnly
	v.Painter.LayoutOnly = true
	rows := render()
	count := len(rows)
	if limit > 0 {
		count = min(count, limit)
	}
	if start < v.syntaxWindow.last && start+count > v.syntaxWindow.first {
		v.Painter.LayoutOnly = false
		rows = render()
	}
	v.Painter.LayoutOnly = saved
	return rows
}

// liveActivityPassing is a Main item that shortens once it has scrolled out
// of view: its first entry and the feed row after it.
type liveActivityPassing struct {
	seq uint64
	end int
}

func (feed *Feed) separator() {
	feed.Heads = append(feed.Heads, len(feed.Lines))
	feed.Lines = append(feed.Lines, "")
	feed.Snippets = append(feed.Snippets, Snippet{})
	feed.Questions = append(feed.Questions, 0)
}

// appendRows keeps row content and metadata aligned; each renderer owns heads.
func (feed *Feed) appendRows(run Run) {
	feed.Lines = append(feed.Lines, run.Lines...)
	feed.Snippets = append(feed.Snippets, run.Snippets...)
	feed.Questions = append(feed.Questions, run.Questions...)
}

// RenderFeed groups consecutive entries of one agent under a heading with a
// colored gutter. Adjacent reads collapse into one row. In the interleaved
// view each block is clipped to five rows; full content opens in a dialog.
func (v *View) RenderFeed(width, rows int) Feed {
	// Elapsed labels can shrink at unit boundaries. Layout and deferred paint
	// must measure them at the same instant so their row counts stay aligned.
	now, clock := v.Painter.now(), v.Painter.Clock
	v.Painter.Clock = func() time.Time { return now }
	defer func() { v.Painter.Clock = clock }()

	v.Painter.LayoutOnly = true
	feed := v.LayoutFeed(width)
	v.Painter.LayoutOnly = false
	if anchor := v.ExpansionAnchor; anchor != nil {
		for _, span := range feed.Spans {
			if span.Seq == anchor.Seq {
				v.Offset = span.Start + min(anchor.Start, max(0, span.End-span.Start-1))
				break
			}
		}
	}
	offset, _ := v.viewportPosition(feed, rows)
	for _, paint := range feed.paints {
		old := v.Runs[paint.key]
		decorate := func(first, last int) {
			if first >= last {
				return
			}
			if old.painted == nil {
				old.painted = make([]bool, len(old.Lines))
			}
			if !slices.Contains(old.painted[first:last], false) {
				return
			}
			v.syntaxWindow = &liveActivitySyntaxWindow{first, last}
			var run Run
			if paint.render != nil {
				run = paint.render()
			} else {
				run = v.renderConversationRun(paint.conversation, paint.key)
			}
			v.syntaxWindow = nil
			copy(old.Lines[first:last], run.Lines[first:last])
			for i := first; i < last; i++ {
				old.painted[i] = true
			}
			copy(feed.Lines[paint.start+first:paint.start+last], run.Lines[first:last])
		}
		decorate(max(0, offset-paint.start), min(len(old.Lines), offset+rows-paint.start))
		old.Colored = old.painted != nil && !slices.Contains(old.painted, false)
		v.Runs[paint.key] = old
	}
	feed.paints = nil
	if len(v.outputDemands) == 0 {
		v.ExpansionAnchor = nil
	}
	return feed
}

func (v *View) LayoutFeed(width int) Feed {
	if v.Conversation {
		return v.RenderConversation(width)
	}
	v.beginRuns()
	clip := 5
	if v.Expansion != 0 {
		clip = 0
	}
	// A cross-pane jump flashes its target from the first frame that shows it.
	if v.PendingTarget != 0 && slices.ContainsFunc(v.entries, func(entry activitypresentation.Entry) bool {
		return entry.Seq == v.PendingTarget && v.Visible(entry)
	}) {
		v.FlashQuestion, v.FlashUntil = v.PendingTarget, v.Now().Add(700*time.Millisecond)
	}
	flash := uint64(0)
	if v.Now().Before(v.FlashUntil) {
		flash = v.FlashQuestion
	}
	var feed Feed
	lastPreview := make(map[string]int)
	for i, entry := range v.entries {
		if v.Visible(entry) && len(v.LivePreviews[entry.Agent]) > 0 {
			lastPreview[entry.Agent] = i
		}
	}
	appendPreview := func(agent string) {
		if len(feed.Lines) > 0 {
			feed.separator()
		}
		head := len(feed.Lines)
		lines := v.LivePreviews[agent]
		for range lines {
			feed.Heads = append(feed.Heads, head)
		}
		feed.appendRows(Run{Lines: lines, Snippets: make([]Snippet, len(lines)), Questions: make([]uint64, len(lines))})
	}
	for i := 0; i < len(v.entries); {
		if !v.Visible(v.entries[i]) {
			i++
			continue
		}
		agent := v.entries[i].Agent
		shared := v.sharedEvent(i)
		if last, ok := lastPreview[agent]; ok {
			if i == last {
				appendPreview(agent)
			}
			i++
			continue
		}
		last, j := i, i
		for ; j < len(v.entries); j++ {
			if !v.Visible(v.entries[j]) {
				continue
			}
			next := v.entries[j]
			if next.Agent != agent || j != i && (shared || v.sharedEvent(j)) {
				break
			}
			last = j
		}
		key := RunKey{First: v.entries[i].Seq, Last: v.entries[last].Seq, Revision: v.runRevision(i, last+1), Width: width, Clip: clip, Theme: v.Painter.Theme, Hover: -1, Tail: v.tailRows(), Expansion: v.Expansion}
		if flash != 0 && slices.ContainsFunc(v.entries[i:j], func(entry activitypresentation.Entry) bool { return entry.Seq == flash }) {
			key.Flash = flash
		}
		if v.Snippet.Run == key.First {
			key.Hover = v.Snippet.Block
		}
		run, ok := v.Runs[key]
		for k := i; k <= last; k++ {
			if v.entries[k].Running {
				ok = false
				break
			}
		}
		first := i // The deferred painter must not borrow the advancing loop cursor.
		render := func() Run {
			if shared {
				return v.activityEventItem(first, width, clip, key.Flash)
			}
			var blocks []Block
			for k := first; k <= last; k++ {
				if v.Visible(v.entries[k]) {
					for _, block := range v.shownBlocks(k) {
						block.Source = v.entries[k].Seq
						block.Flash = block.Source == key.Flash
						block.TailRows = key.Tail
						blocks = append(blocks, block)
					}
				}
			}
			observed := v.entries[last].Observed
			if v.ChildrenOnly {
				observed = v.entries[first].Observed // A stable heading while the run grows.
			}
			if v.Expansion == 0 {
				blocks = MergeTargets(blocks)
			}
			return v.renderRun(key.First, agent, observed, blocks, width, clip)
		}
		if !ok {
			run = render()
			run.Colored = !v.Painter.LayoutOnly
		}
		run.generation = v.runGeneration
		v.Runs[key] = run
		if len(feed.Lines) > 0 {
			feed.separator()
		}
		head := len(feed.Lines)
		if full := key; !ok && key.Excerpt && !v.Following && head < v.Offset {
			full.Excerpt = false
			if shown, ok := v.Runs[full]; ok {
				v.Offset -= len(shown.Lines) - len(run.Lines)
			}
		}
		if !run.Colored {
			feed.paints = append(feed.paints, liveActivityPaint{start: head, key: key, render: render})
		}
		for seq, row := range run.EntryRows {
			v.QuestionRows[seq] = head + row
		}
		for range run.Lines {
			feed.Heads = append(feed.Heads, head)
		}
		feed.appendRows(run)
		feed.Spans = append(feed.Spans, Span{key.First, head, len(feed.Lines)})
		i = j
	}
	// A preview can arrive before the caller's first transcript entry.
	for _, agent := range slices.Sorted(maps.Keys(v.LivePreviews)) {
		if _, ok := lastPreview[agent]; !ok {
			appendPreview(agent)
		}
	}
	v.retainRuns()
	return feed
}

func (v *View) renderRun(first uint64, agent string, observed time.Time, blocks []Block, width, clip int) Run {
	head := v.Painter.Agent(agent)
	detail := ""
	if i := slices.IndexFunc(v.roster, func(row activitypresentation.AgentRow) bool { return row.Agent.Name == agent }); i >= 0 {
		if role := Role(v.roster[i].Agent); role != "" {
			detail = "· " + role
		}
	}
	heading := EventHeading(Gutter(agent, v.Painter.Theme)+"●"+terminalui.Reset, head, detail, observed, width)

	run := Run{
		Lines:     []string{heading},
		Snippets:  make([]Snippet, 1),
		Questions: make([]uint64, 1),
		EntryRows: make(map[uint64]int),
	}
	gutter := Gutter(agent, v.Painter.Theme) + "▎" + terminalui.Reset + " "
	if v.ChildrenOnly {
		gutter = Gutter(agent, v.Painter.Theme) + "│" + terminalui.Reset + " "
	}
	previousMessage, previousSummary := false, false
	operation := func(block Block) bool { return block.Kind == "op" || block.Kind == "reads" }
	// An operation group's later rows and output notes continue the branch above.
	continued := func(block Block) bool {
		return block.Effect || block.Kind == "filter" || block.GroupHeader != "" && !block.GroupStart
	}
	rail := ""
	if v.Expansion == 0 {
		blocks = CompactReasoning(blocks)
	}
	blocks = GroupOperations(blocks)
	if v.Expansion != 2 {
		blocks = collapseCapturedEdits(blocks)
	}
	blocks = AlignVerbs(blocks)
	run.Blocks = blocks
	for index, block := range blocks {
		block = v.discloseBlock(block)
		v.Painter.CopyScope = first + uint64(index)
		// Native Activity joins consecutive operations into one tree.
		tree := v.ChildrenOnly && operation(block) && !continued(block)
		toggle := v.ClickTarget(&block, Snippet{Run: first, Block: index}, width-2)
		if operation(block) && v.Expansion != 2 {
			block.SourceRows = 5
			if block.TailRows == 0 || block.TailRows > 5 {
				block.TailRows = 5
			}
		}
		message := slices.Contains([]string{"text", "message", "final", "summary", "start"}, block.Kind)
		// Reasoning heads the operations after it, so no gap parts them.
		headed := previousSummary && operation(block)
		gap := len(run.Lines) > 1 && (message || previousMessage) && !headed
		start := len(run.Lines)
		if gap {
			start++
		}
		limit := clip
		if operation(block) {
			limit = 0
		} // Source and output have separate budgets.
		part := v.paintBlock(start, limit, func() []string {
			block := v.outputContext(block)
			switch {
			case tree:
				return v.Painter.Event(block, width-4)
			case v.ChildrenOnly:
				return v.Painter.Event(block, width-2)
			default:
				return v.Painter.Block(block, width-2)
			}
		})
		if len(part) == 0 {
			continue
		}
		if gap {
			run.Lines = append(run.Lines, gutter)
			run.Snippets = append(run.Snippets, Snippet{})
			run.Questions = append(run.Questions, 0)
		}
		previousMessage, previousSummary = message, block.Kind == "summary"
		// A merged read row stands for each entry it merged.
		for _, member := range append([]activitypresentation.Value{block.Value}, block.Members...) {
			if member.Source != 0 {
				if _, exists := run.EntryRows[member.Source]; !exists {
					run.EntryRows[member.Source] = len(run.Lines)
				}
			}
		}
		var snippet Snippet
		if toggle {
			snippet = Snippet{Run: first, Block: index}
		}
		if limit > 0 && len(part) > limit {
			// An operation shows in full in the output dialog.
			snippet = Snippet{Run: first, Block: index}
			hint := Elision{Hidden: len(part) - limit + 1, Hovered: snippet == v.Snippet}
			part = append(part[:limit-1:limit-1], hint.String())
		}
		if block.EditOperation() {
			snippet = Snippet{Run: block.Source, Block: EditNavigationSnippet, Path: block.EditPath()}
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
			tail := Tree([][]string{part, {""}})
			if last {
				tail = Tree([][]string{part})
			}
			part, rail = tail[:len(part)], "│ "
			if last {
				rail = "  "
			}
		case v.ChildrenOnly && continued(block) && rail != "":
			for k := range part {
				part[k] = terminalui.Dim + rail + terminalui.Undim + part[k]
			}
		default:
			rail = ""
		}
		for _, line := range part {
			run.Lines = append(run.Lines, gutter+ansi.Truncate(line, width-2, "…"))
			run.Snippets = append(run.Snippets, snippet)
			run.Questions = append(run.Questions, 0)
		}
	}
	return run
}

// viewportPosition resolves scrolling and pending navigation targets without
// committing viewport geometry.
func (v *View) viewportPosition(feed Feed, rows int) (int, bool) {
	offset, following := v.Offset, v.Following
	if target, ok := v.QuestionRows[v.PendingTarget]; v.PendingTarget != 0 && ok {
		offset, following = max(0, target-1), false
	}
	if following {
		offset = len(feed.Lines) - rows
	}
	return max(0, min(offset, len(feed.Lines)-rows)), following
}

// Viewport returns exactly rows lines. When scrolled into a run, that run's
// heading stays pinned on the first row.
func (v *View) Viewport(feed Feed, rows int) []string {
	rows = max(0, rows)
	if len(v.outputDemands) > 0 {
		return make([]string, rows)
	}
	v.Offset, v.Following = v.viewportPosition(feed, rows)
	v.imageViewport(feed, rows)
	v.PendingTarget = 0
	v.FeedSpans = feed.Spans
	v.FeedLines, v.FeedRows = len(feed.Lines), max(1, rows)
	if v.Following {
		v.Unseen = 0
	}
	for _, item := range feed.Passing {
		if item.end <= v.Offset {
			if v.Passed == nil {
				v.Passed = make(map[uint64]bool)
			}
			v.Passed[item.seq] = true
		}
	}
	lines := make([]string, rows)
	v.FeedSnippets = make([]Snippet, rows)
	v.FeedQuestions = make([]uint64, rows)
	for row := range rows {
		index := v.Offset + row
		if index < len(feed.Lines) {
			lines[row], v.FeedSnippets[row] = feed.Lines[index], feed.Snippets[index]
			v.FeedQuestions[row] = feed.Questions[index]
			if target := feed.Snippets[index]; row == v.EditHover-1 && target.Block == EditNavigationSnippet && target == v.Snippet {
				lines[row] = UnderlineEdit(lines[row])
			}
			if row == v.QuestionHover-1 && feed.Questions[index] != 0 {
				lines[row] = underlineLink(lines[row])
			}
		}
	}
	// Pin only when the run keeps a visible line under its heading. Main's
	// transcript items do not always start with a heading, so it never pins.
	if !v.Conversation && rows > 1 && v.Offset+1 < len(feed.Heads) && feed.Heads[v.Offset] != v.Offset && feed.Heads[v.Offset+1] == feed.Heads[v.Offset] {
		lines[0], v.FeedSnippets[0] = feed.Lines[feed.Heads[v.Offset]], Snippet{}
		v.FeedQuestions[0] = 0
	}
	// Activity's painter flashes its own entries; Main flashes whole items.
	if target, ok := v.QuestionRows[v.FlashQuestion]; ok && v.Conversation && v.Now().Before(v.FlashUntil) {
		for row := range lines {
			index := v.Offset + row
			if index < len(feed.Heads) && feed.Heads[index] == target {
				lines[row] = v.selectRow(lines[row], ansi.StringWidth(lines[row]))
			}
		}
	}
	return lines
}

func (v *View) ExpireFlash(now time.Time) bool {
	if v.FlashQuestion == 0 || now.Before(v.FlashUntil) {
		return false
	}
	v.FlashQuestion, v.FlashUntil = 0, time.Time{}
	return true
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (v *View) Footer(width int) string {
	mode, toggle := "ALL", "a only"
	if v.displayingChild() {
		mode, toggle = "ONLY", "a all"
	}
	keys := "n/p agent · " + toggle + " · j/k scroll · End bottom"
	if !v.FeedOnly {
		keys = "click agent · " + keys
	}

	if width < 60 {
		keys = "n/p · a · j/k · End"
	}
	return ansi.Truncate("\x1b[1m"+mode+terminalui.Undim+terminalui.Dim+" · "+keys+terminalui.Undim, width, "…")
}

// Middle shortens a path while keeping its leaf name visible.
func Middle(name string, width int) string {
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

// ShowAgent steps through child transcripts, with Main before the first child.
func (v *View) ShowAgent(step int) {
	rows := v.feedAgents()
	if len(rows) == 0 {
		return
	}
	index := -1
	if v.displayingChild() {
		index = slices.IndexFunc(rows, func(row activitypresentation.AgentRow) bool { return row.Agent.Name == v.displayedCaller() })
	}
	index = max(-1, min(index+step, len(rows)-1))
	v.Hovered = ""
	v.RosterManual = false
	if index < 0 {
		v.requestDisplay("/root")
	} else {
		v.requestDisplay(rows[index].Agent.Name)
	}
	v.Follow()
}

// HandleRosterKey interprets a complete key from the attached terminal decoder.
func (v *View) HandleRosterKey(key string) bool {
	switch key {
	case "\x03", "q":
		return true
	case "j", "n", "\t", "\x1b[B", "\x1bOB":
		v.ShowAgent(1)
	case "k", "p", "\x1b[A", "\x1bOA":
		v.ShowAgent(-1)
	case "a":
		if v.displayingChild() {
			v.requestDisplay("/root")
		} else {
			v.requestDisplay(v.Selected)
		}
		v.Follow()
	}
	return false
}
