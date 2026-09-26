package router

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

const (
	liveActivityFeedLimit = 2000
	// Panes at least this wide place agent cards beside the feed.
	liveActivitySideColumns = 100
)

// Main and Activity each own one of these views, sharing the renderer. Entries carry
// sequence numbers, so a reconnect snapshot merges without duplicating retained rows.
type liveActivityView struct {
	agents         []activityPaneAgent
	entries        []activityPaneEntry
	blocks         [][]liveActivityBlock // Parsed entries, aligned with entries.
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
	unseen         int
	status         string
	roleColors     map[string]string
	feedOnly       bool
	conversation   bool              // Main uses the same feed/state with full, unclipped messages.
	childrenOnly   bool              // Native Main already owns root activity; keep it out of the auxiliary feed.
	bare           bool              // The shell's pane title replaces the heading and footer rows.
	focused        bool              // Native Activity shows its key hints only while it has keyboard focus.
	mainView       *liveActivityView // Roster reads Main's state without duplicating its feed entries.
	painter        liveActivityPainter
	osc            livediff.OSC
	runs           map[liveActivityRunKey]liveActivityRun

	// expanded snippets show in full in the shared feed; snippet is the
	// hovered collapsed one.
	expanded map[liveActivitySnippet]bool
	snippet  liveActivitySnippet

	// Geometry of the last frame, used by scrolling keys and the pointer.
	// feedSnippets holds each feed row's snippet, from screen row feedTop
	// between columns feedLeft and feedRight.
	feedLines, feedRows int
	feedSnippets        []liveActivitySnippet
	feedQuestions       []uint64
	questionRows        map[uint64]int
	// questionHover is the pointed feed line plus one; zero points at none.
	questionHover                        int
	flashQuestion                        uint64
	flashUntil                           time.Time
	feedTop, feedLeft, feedRight         int
	rosterTop, rosterBottom, rosterRight int
	width, height                        int
}

// liveActivitySnippet names a clippable block in the shared feed: the
// sequence of its run's first entry and its index in the run. Sequences start
// at one, so the zero value names no snippet.
type liveActivitySnippet struct {
	run   uint64
	block int
}

type liveActivityRun struct {
	lines     []string
	snippets  []liveActivitySnippet // Aligned with lines.
	questions []uint64              // Clickable question targets, aligned with lines.
}

type liveActivityRosterRow struct {
	agent activityPaneAgent
	depth int
}

type liveActivityHit struct {
	row, first, last int // One-based terminal coordinates, inclusive.
	agent            string
}

type liveActivityRunKey struct {
	first, last uint64
	width, clip int
	theme       livediff.Theme
	hover       int // Hovered snippet block in this run, or -1.
	main        bool
}

func newLiveActivityView() *liveActivityView {
	return &liveActivityView{
		following: true, status: "CONNECTING",
		painter: liveActivityPainter{theme: livediff.EnvironmentTheme(os.Getenv("COLORFGBG"))},
	}
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
		if entry.Kind != "reasoning" && v.mergeNative(entry) {
			continue
		}
		if entry.Kind == "reasoning" {
			// These are provider-visible summaries from the collector, never
			// raw reasoning. Keep legacy panes and the other audience excluded.
			if !(v.childrenOnly && entry.Agent != "/root" || v.conversation && entry.Agent == "Main") {
				continue
			}
			updated := false
			if entry.CallID != "" {
				for i, previous := range v.entries {
					if previous.Kind == "reasoning" && previous.Agent == entry.Agent && previous.CallID == entry.CallID &&
						(previous.native == nil && entry.native == nil || previous.native != nil && entry.native != nil && previous.native.sameItem(entry.native)) {
						entry.Seq = previous.Seq
						if reasoningSummaryHeader(previous.Text) == reasoningSummaryHeader(entry.Text) {
							entry.Observed = previous.Observed
						}
						v.entries[i], v.blocks[i], v.runs = entry, parseLiveActivity(entry), nil
						updated = true
						break
					}
				}
			}
			if updated {
				continue
			}
		}
		if entry.Kind == "output_filter" && entry.CallID != "" {
			matched := false
			for i, previous := range slices.Backward(v.entries) {
				if previous.Kind == "tool" && previous.Agent == entry.Agent && previous.CallID == entry.CallID {
					blocks := parseLiveActivity(entry)
					if len(blocks) == 2 && blocks[1].kind == "filter" {
						v.blocks[i] = append(v.blocks[i], blocks[1])
						v.runs = nil
						matched = true
					}
					break
				}
			}
			if matched {
				continue
			}
		}
		if entry.Kind == "exit" {
			for i, v0 := range slices.Backward(v.entries) {
				if v0.Agent == entry.Agent && v0.CallID == entry.CallID && entry.CallID != "" {
					for j := range v.blocks[i] {
						if v.blocks[i][j].verb == "Run" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, v.blocks[i][j].verb) {
							v.blocks[i][j].exitCode, _ = strconv.Atoi(entry.Text)
						}
					}
					v.runs = nil
					break
				}
			}
			continue
		}
		blocks := parseLiveActivity(entry)
		if entry.Kind == "tool" && entry.CallID != "" && len(blocks) > 0 &&
			slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, blocks[0].verb) {
			// A confirmed edit receipt replaces the provisional Run row for
			// this call. The capturer owns the action and counts; display does
			// not classify the shell command a second time.
			for i := len(v.entries) - 1; i >= 0; i-- {
				prior := v.entries[i]
				if prior.Kind != "tool" || prior.Agent != entry.Agent || prior.CallID != entry.CallID ||
					len(v.blocks[i]) == 0 || v.blocks[i][0].verb != "Run" {
					continue
				}
				entry.Seq = prior.Seq
				blocks[0].exitCode = v.blocks[i][0].exitCode
				for _, annotation := range v.blocks[i][1:] {
					if annotation.kind == "filter" {
						blocks = append(blocks, annotation)
					}
				}
				v.entries[i], v.blocks[i], v.runs = entry, blocks, nil
				blocks = nil
				break
			}
			if blocks == nil {
				continue
			}
		}
		v.entries = append(v.entries, entry)
		v.blocks = append(v.blocks, blocks)
		if !v.following && v.visible(entry) {
			v.unseen++
		}
	}
	if extra := len(v.entries) - liveActivityFeedLimit; extra > 0 {
		v.entries = slices.Delete(v.entries, 0, extra)
		v.blocks = slices.Delete(v.blocks, 0, extra)
		v.runs = nil
		for snippet := range v.expanded {
			if snippet.run < v.entries[0].Seq {
				delete(v.expanded, snippet)
			}
		}
	}
	v.keepSelection()
	return false
}

// keepSelection falls back to the first agent when the selection is unknown.
func (v *liveActivityView) keepSelection() {
	if v.selected == "" || !slices.ContainsFunc(v.agents, func(a activityPaneAgent) bool { return a.Name == v.selected }) {
		if rows := v.roster(); len(rows) > 0 {
			v.selected = rows[0].agent.Name
		}
	}
}

func (v *liveActivityView) visible(entry activityPaneEntry) bool {
	if entry.Kind == "reasoning" {
		if v.conversation {
			return entry.Agent == "Main"
		}
		if !v.childrenOnly {
			return false
		}
		if v.only {
			return entry.Agent == v.selected
		}
		// Like Codex's live status, this row disappears when later activity
		// supersedes it. The summary body remains in the detailed view.
		latest := v.latest(entry.Agent)
		return latest >= 0 && v.entries[latest].Seq == entry.Seq && slices.ContainsFunc(v.agents, func(agent activityPaneAgent) bool {
			return agent.Name == entry.Agent && agent.Responding
		})
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
		return v.roleColor(role) + string(v.agentStatus(agent)) + liveActivityReset
	}
	switch v.agentStatus(agent) {
	case '◐':
		return liveActivityAmber + "◐" + liveActivityReset
	case '!':
		return liveActivityRed + "!" + liveActivityReset
	case '✓':
		return liveActivityGreen + "✓" + liveActivityReset
	}
	return liveActivityDim + "·" + liveActivityUndim
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
	rows := v.roster()
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
		return v.scrollKey(paneWheelKey(action))
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
				v.flashUntil = time.Now().Add(700 * time.Millisecond)
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
		hover = v.offset + index + 1
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
	return lead + "\x1b[4m" + strings.ReplaceAll("↩"+link, liveActivityReset, liveActivityReset+"\x1b[4m") + "\x1b[24m"
}

// pointSnippet underlines a hovered collapsed snippet. A click expands a
// collapsed snippet or collapses an expanded one.
func (v *liveActivityView) pointSnippet(action byte, row, column int) bool {
	var snippet liveActivitySnippet
	if index := row - v.feedTop; index >= 0 && index < len(v.feedSnippets) && column >= v.feedLeft && column <= v.feedRight {
		snippet = v.feedSnippets[index]
	}
	redraw := false
	if action == '\r' && snippet != (liveActivitySnippet{}) {
		if v.expanded[snippet] {
			delete(v.expanded, snippet)
		} else {
			if v.expanded == nil {
				v.expanded = make(map[liveActivitySnippet]bool)
			}
			v.expanded[snippet] = true
			// Hold the feed still so the expanded lines open below the pointer.
			v.scroll(0)
		}
		for key := range v.runs {
			if key.first == snippet.run {
				delete(v.runs, key)
			}
		}
		redraw = true
	}
	if v.expanded[snippet] {
		snippet = liveActivitySnippet{}
	}
	if snippet != v.snippet {
		v.snippet, redraw = snippet, true
	}
	return redraw
}

// pointAgent highlights a hovered roster agent; a click filters the feed.
func (v *liveActivityView) pointAgent(action byte, row, column int) bool {
	previous := v.hovered
	v.hovered = ""
	for _, hit := range v.hits {
		if hit.row == row && column >= hit.first && column <= hit.last {
			v.hovered = hit.agent
			if action == '\r' {
				v.only = !v.only || v.selected != hit.agent
				v.selected = hit.agent
				v.rosterManual = false
				v.hovered = ""
				v.follow()
			}
			return action == '\r' || previous != v.hovered
		}
	}
	return previous != ""
}

func (v *liveActivityView) follow() {
	v.following, v.unseen = true, 0
}

func (v *liveActivityView) scrollKey(key byte) bool {
	offset := v.offset
	if v.following {
		offset = max(0, v.feedLines-v.feedRows)
	}
	next, follow, ok := paneScroll(key, offset, v.feedRows, v.feedLines)
	if ok {
		v.offset = max(0, min(next, v.feedLines-v.feedRows))
		v.following = follow
		if follow {
			v.unseen = 0
		}
	}
	return ok
}

func (v *liveActivityView) scroll(delta int) {
	if v.following {
		v.offset = max(0, v.feedLines-v.feedRows)
	}
	v.following = false
	v.offset = max(0, min(v.offset+delta, v.feedLines-v.feedRows))
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
		lines = append(lines, v.viewport(v.renderFeed(text, body), body)...)
	case len(rows) > 0 && text >= liveActivitySideColumns && body >= 6:
		cardWidth := min(44, max(28, text*3/10))
		feedWidth := text - cardWidth - 3
		cards := v.renderCards(rows, cardWidth, body, now)
		v.rosterTop, v.rosterBottom, v.rosterRight = 2, body+1, cardWidth
		feed := v.viewport(v.renderFeed(feedWidth, body), body)
		v.feedLeft = cardWidth + 4
		for i := range body {
			lines = append(lines, liveActivityPad(cards[i], cardWidth)+liveActivityDim+" │ "+liveActivityUndim+feed[i])
		}
	case len(rows) > 0 && body >= 8:
		roster := v.renderRoster(rows, text, max(2, body/3), now)
		v.rosterTop, v.rosterBottom, v.rosterRight = 2, len(roster)+1, text
		feedRows := body - len(roster) - 1
		lines = append(lines, roster...)
		lines = append(lines, liveActivityDim+strings.Repeat("─", text)+liveActivityUndim)
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
	return lines
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
		return v.activityHeader(rows, width)
	}
	left := "\x1b[1m" + v.painter.theme.Accent() + title + liveActivityReset
	responding, errors := v.statusCounts(rows)
	switch {
	case v.feedOnly && !rosterOnly && !v.only:
		left += " · all"
	case len(rows) == 0:
		left += liveActivityDim + " · waiting for subagent activity" + liveActivityUndim
	case v.only && !rosterOnly:
		index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
		left += fmt.Sprintf(" · only %s %s(%d/%d)%s", v.painter.agent(v.selected), liveActivityDim, index+1, len(rows), liveActivityUndim)
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
		right = liveActivityAmber + "PAUSED" + liveActivityReset
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
func (v *liveActivityView) activityHeader(rows []liveActivityRosterRow, width int) string {
	left := "\x1b[1m" + v.painter.theme.Accent() + "Activity" + liveActivityReset
	if v.only {
		index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
		left += liveActivityDim + " · only " + liveActivityUndim + v.painter.agent(v.selected) + liveActivityDim + fmt.Sprintf(" %d/%d", index+1, len(rows)) + liveActivityUndim
	}
	right := v.status
	if right == "" && !v.following {
		right = liveActivityAmber + "paused" + liveActivityReset
		if v.unseen > 0 {
			right += liveActivityAmber + fmt.Sprintf(" · %d new", v.unseen) + liveActivityReset
		}
		right += liveActivityDim + " · r follows" + liveActivityUndim
	}
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if right == "" || gap < 2 {
		return ansi.Truncate(left, width, "…")
	}
	return left + strings.Repeat(" ", gap) + right
}

// current is an agent's latest activity summary and its elapsed/response timer.
func (v *liveActivityView) current(agent activityPaneAgent, now time.Time) (string, string) {
	summary := liveActivityDim + "—" + liveActivityUndim
	source := v
	if agent.Name == "/root" && v.mainView != nil {
		source = v.mainView
		if source.status != "" {
			summary = source.status
		}
	}
	for i, v0 := range slices.Backward(source.entries) {
		blocks := source.blocks[i]
		if v0.Agent == agent.Name || agent.Name == "/root" && source != v && v0.Agent == "Main" {
			summary = v.painter.summary(blocks)
			if v0.Kind == "reasoning" && agent.Responding {
				summary = reasoningShimmer(reasoningSummaryHeader(v0.Text), now.Sub(v0.Observed), v.painter.colors)
			}
			break
		}
		if agent.Name == "/root" && len(blocks) == 1 && blocks[0].kind == "message" && blocks[0].to == "/root" {
			block := blocks[0]
			block.owner = "/root"
			summary = v.painter.summary([]liveActivityBlock{block})
			break
		}
	}
	if agent.Started.IsZero() {
		if latest := v.latest(agent.Name); latest >= 0 {
			return summary, liveActivityAge(max(0, now.Sub(v.entries[latest].Observed))) + " · —"
		}
		return summary, ""
	}
	last := "—"
	if !agent.LastResponse.IsZero() {
		last = liveActivityLast(agent.LastResponse, now)
	}
	// Elapsed time stops when the agent stops responding; the last-response
	// age keeps counting.
	end := now
	if !agent.Responding && !agent.LastResponse.IsZero() {
		end = agent.LastResponse
	}
	return summary, liveActivityAge(max(0, end.Sub(agent.Started))) + " · " + last
}

// liveActivityTokens shows cumulative input (sent) and output (received) tokens.
func liveActivityTokens(agent activityPaneAgent) string {
	if agent.InputTokens == 0 && agent.OutputTokens == 0 {
		return ""
	}
	return "↑ " + formatUsageTokens(agent.InputTokens) + " ↓ " + formatUsageTokens(agent.OutputTokens)
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

// liveActivityTurns counts provider responses as T+N.
func liveActivityTurns(agent activityPaneAgent) string {
	if agent.Turns == 0 {
		return ""
	}
	return liveActivityDim + "T+" + liveActivityUndim + fmt.Sprint(agent.Turns)
}

// selectRow fills the selected agent's rows, so selection takes no column of
// its own. Resets inside the row restore the fill.
func (v *liveActivityView) selectRow(line string, width int) string {
	fill := v.painter.theme.SelectionBackground()
	line = strings.ReplaceAll(line, liveActivityReset, liveActivityReset+fill)
	return fill + line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line))) + "\x1b[49m"
}

func (v *liveActivityView) hoverName(name, agent string) string {
	if agent == v.hovered {
		return "\x1b[4m" + name + "\x1b[24m"
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
		return agentDisplayName(rows[index].agent.Name), ""
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
	return liveActivityDim + line + liveActivityUndim
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
		summary, _ := v.current(row.agent, now)
		name := names[i-start]
		available := nameWidth
		if cards && !compact {
			available = max(1, width-3)
		}
		name = strings.TrimRight(liveActivityMiddle(name, available), " ")
		split := strings.LastIndexAny(name, " /") + 1
		styled := liveActivityDim + name[:split] + liveActivityUndim + liveAgentColor(row.agent.Name) + v.hoverName(name[split:], row.agent.Name) + liveActivityReset
		prefix := " " + v.glyph(row.agent) + " "
		switch {
		case cards && !compact:
			last := liveActivityLast(row.agent.LastResponse, now)
			gap := width - 3 - ansi.StringWidth(name) - ansi.StringWidth(last)
			line := prefix + styled
			if gap >= 2 {
				line += strings.Repeat(" ", gap) + liveActivityDim + last + liveActivityUndim
			}
			add(line)
			add("   " + liveActivityDim + indents[i-start] + liveActivityUndim + summary)
		case cards:
			add(prefix + styled + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name))) + "  " + summary)
		default:
			line := prefix + styled + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name))) + "  "
			metric := table[i-start]
			room := width - ansi.StringWidth(line) - 2 - ansi.StringWidth(metric)
			if metric == "" || room < 20 {
				add(line + summary)
			} else {
				add(line + liveActivityPad(summary, room) + "  " + metric)
			}
		}
		if cards && (!compact || (metrics && i == selected)) {
			add("   " + liveActivityDim + indents[i-start] + liveActivityUndim + liveActivityCardMetrics(row.agent))
		}
		if i == selected {
			for j := first - 2; j < len(lines); j++ {
				lines[j] = v.selectRow(lines[j], width)
			}
		}
		for hit := first; hit < len(lines)+2; hit++ {
			v.hits = append(v.hits, liveActivityHit{hit, 1, width, row.agent.Name})
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
			tokens = liveActivityDim + "↑ " + liveActivityUndim + liveActivityPad(formatUsageTokens(row.agent.InputTokens), 6) + liveActivityDim + " ↓ " + liveActivityUndim + liveActivityPad(formatUsageTokens(row.agent.OutputTokens), 6)
		}
		timerCell := ""
		if timer != "" {
			elapsed, age, _ := strings.Cut(timer, " · ")
			timerCell = strings.Repeat(" ", max(0, 3-len(elapsed))) + liveActivityMetricValues(elapsed) + liveActivityDim + " · " + liveActivityUndim + liveActivityMetricValues(age)
		}
		cost, turns := "", ""
		if row.agent.Turns > 0 && row.agent.CostKnown {
			prefix := " "
			if row.agent.CostPartial {
				prefix = "≥"
			}
			cost = prefix + "$" + fmt.Sprintf("%.2f", row.agent.Cost)
		}
		if row.agent.Turns > 0 {
			turns = liveActivityDim + "T+" + liveActivityUndim + fmt.Sprint(row.agent.Turns)
		}
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

// Keep numerical values bright while labels and units recede.
func liveActivityMetricValues(text string) string {
	fields := strings.Fields(text)
	for i, field := range fields {
		n := 0
		for n < len(field) && (field[n] >= '0' && field[n] <= '9' || field[n] == '.') {
			n++
		}
		fields[i] = field[:n] + liveActivityDim + field[n:] + liveActivityUndim
	}
	return strings.Join(fields, " ")
}

func liveActivityCardMetrics(agent activityPaneAgent) string {
	var parts []string
	if cost := liveActivityCost(agent); cost != "" {
		parts = append(parts, cost)
	} else if agent.InputTokens > 0 {
		parts = append(parts, liveActivityDim+"↑ "+liveActivityUndim+formatUsageTokens(agent.InputTokens))
	}
	if turns := liveActivityTurns(agent); turns != "" {
		parts = append(parts, turns)
	}
	return strings.Join(parts, liveActivityDim+" · "+liveActivityUndim)
}

// renderStrip is the one-line roster for short panes.
func (v *liveActivityView) renderStrip(rows []liveActivityRosterRow, width int) string {
	var parts []string
	column := 1
	for _, row := range rows {
		name := agentDisplayName(row.agent.Name)
		if row.agent.Name == v.selected || row.agent.Name == v.hovered {
			name = "\x1b[4m" + name + "\x1b[24m"
		}
		part := v.glyph(row.agent) + " " + liveAgentColor(row.agent.Name) + name + liveActivityReset
		if last := min(width, column+ansi.StringWidth(part)-1); column <= last {
			v.hits = append(v.hits, liveActivityHit{2, column, last, row.agent.Name})
		}
		parts = append(parts, part)
		column += ansi.StringWidth(part) + 2
	}
	return ansi.Truncate(strings.Join(parts, "  "), width, "…")
}

type liveActivityFeed struct {
	lines     []string
	heads     []int                 // Index of the heading that owns each line.
	snippets  []liveActivitySnippet // Snippet that owns each line, if any.
	questions []uint64
}

// renderFeed groups consecutive entries of one agent under a heading with a
// colored gutter. Adjacent reads collapse into one row. In the interleaved
// view each block is clipped to a share of the feed that grows with the pane,
// unless the viewer expanded it.
func (v *liveActivityView) renderFeed(width, rows int) liveActivityFeed {
	if v.conversation {
		return v.renderConversation(width)
	}
	clip := 0
	if !v.only {
		clip = min(12, max(3, rows/3))
	}
	var feed liveActivityFeed
	v.questionRows = make(map[uint64]int)
	used := make(map[liveActivityRunKey]liveActivityRun)
	for i := 0; i < len(v.entries); {
		if !v.visible(v.entries[i]) {
			i++
			continue
		}
		agent := v.entries[i].Agent
		last, j := i, i
		for ; j < len(v.entries); j++ {
			if !v.visible(v.entries[j]) {
				continue
			}
			if v.entries[j].Agent != agent {
				break
			}
			last = j
		}
		key := liveActivityRunKey{v.entries[i].Seq, v.entries[last].Seq, width, clip, v.painter.theme, -1, false}
		if v.snippet.run == key.first {
			key.hover = v.snippet.block
		}
		run, ok := v.runs[key]
		// Only live compact reasoning runs animate; ordinary history stays cached.
		if clip > 0 {
			for k := i; k <= last; k++ {
				if v.entries[k].Kind == "reasoning" && v.visible(v.entries[k]) {
					ok = false
					break
				}
			}
		}
		if !ok {
			var blocks []liveActivityBlock
			for k := i; k <= last; k++ {
				if v.visible(v.entries[k]) {
					blocks = append(blocks, v.blocks[k]...)
				}
			}
			observed := v.entries[last].Observed
			if v.childrenOnly {
				observed = v.entries[i].Observed // A stable heading while the run grows.
			}
			run = v.renderRun(key.first, agent, observed, mergeLiveActivityReads(blocks), width, clip)
		}
		used[key] = run
		if len(feed.lines) > 0 {
			feed.lines = append(feed.lines, "")
			feed.heads = append(feed.heads, len(feed.lines)-1)
			feed.snippets = append(feed.snippets, liveActivitySnippet{})
			feed.questions = append(feed.questions, 0)
		}
		head := len(feed.lines)
		for range run.lines {
			feed.heads = append(feed.heads, head)
		}
		feed.lines = append(feed.lines, run.lines...)
		feed.snippets = append(feed.snippets, run.snippets...)
		feed.questions = append(feed.questions, run.questions...)
		i = j
	}
	v.runs = used
	return feed
}

func (v *liveActivityView) renderRun(first uint64, agent string, observed time.Time, blocks []liveActivityBlock, width, clip int) liveActivityRun {
	stamp := " " + observed.Local().Format("15:04:05")
	head := liveAgentGutter(agent, v.painter.theme) + "●" + liveActivityReset + " " + v.painter.agent(agent)
	rule := max(1, width-ansi.StringWidth(head)-ansi.StringWidth(stamp)-1)
	heading := head + " " + liveActivityDim + strings.Repeat("─", rule) + stamp + liveActivityUndim
	if v.childrenOnly {
		// The gutter already separates agents; a rule on every run is noise.
		head = v.painter.agent(agent)
		if i := slices.IndexFunc(v.agents, func(a activityPaneAgent) bool { return a.Name == agent }); i >= 0 {
			if role := liveActivityRole(v.agents[i]); role != "" {
				head += liveActivityDim + " · " + role + liveActivityUndim
			}
		}
		heading = head + strings.Repeat(" ", max(1, width-ansi.StringWidth(head)-ansi.StringWidth(stamp))) + liveActivityDim + stamp + liveActivityUndim
	}
	run := liveActivityRun{
		lines:     []string{ansi.Truncate(heading, width, "")},
		snippets:  make([]liveActivitySnippet, 1),
		questions: make([]uint64, 1),
	}
	gutter := liveAgentGutter(agent, v.painter.theme) + "▎" + liveActivityReset + " "
	if v.childrenOnly {
		gutter = liveAgentGutter(agent, v.painter.theme) + "│" + liveActivityReset + " "
	}
	previousMessage := false
	operation := func(block liveActivityBlock) bool { return block.kind == "op" || block.kind == "reads" }
	rail := ""
	for index, block := range blocks {
		block.compact = clip > 0
		// Native Activity joins consecutive operations into one tree.
		tree := v.childrenOnly && operation(block)
		part := v.painter.block(block, width-2)
		switch {
		case tree:
			part = v.painter.event(block, width-4)
		case v.childrenOnly:
			part = v.painter.event(block, width-2)
		}
		if len(part) == 0 {
			continue
		}
		message := slices.Contains([]string{"text", "message", "final", "summary", "start"}, block.kind)
		if len(run.lines) > 1 && (message || previousMessage) {
			run.lines = append(run.lines, gutter)
			run.snippets = append(run.snippets, liveActivitySnippet{})
			run.questions = append(run.questions, 0)
		}
		previousMessage = message
		// Messages carry results, so they get twice the operation share.
		limit := clip
		if block.kind == "message" || block.kind == "final" {
			limit *= 2
		}
		var snippet liveActivitySnippet
		if limit > 0 && len(part) > limit {
			snippet = liveActivitySnippet{first, index}
			if !v.expanded[snippet] {
				hint := fmt.Sprintf("… +%d lines", len(part)-limit+1)
				if snippet == v.snippet {
					hint = "\x1b[4m" + hint + "\x1b[24m"
				}
				part = append(part[:limit-1:limit-1], liveActivityDim+hint+liveActivityUndim)
			}
		}
		switch {
		case tree:
			last := true
			for _, next := range blocks[index+1:] {
				if next.kind != "filter" {
					last = !operation(next)
					break
				}
			}
			tail := liveActivityTree([][]string{part, {""}})
			if last {
				tail = liveActivityTree([][]string{part})
			}
			part, rail = tail[:len(part)], "│ "
			if last {
				rail = "  "
			}
		case v.childrenOnly && block.kind == "filter" && rail != "":
			for k := range part {
				part[k] = liveActivityDim + rail + liveActivityUndim + part[k]
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

// viewport returns exactly rows lines. When scrolled into a run, that run's
// heading stays pinned on the first row.
func (v *liveActivityView) viewport(feed liveActivityFeed, rows int) []string {
	rows = max(0, rows)
	v.feedLines, v.feedRows = len(feed.lines), rows
	if v.following {
		v.offset = max(0, len(feed.lines)-rows)
		v.unseen = 0
	}
	v.offset = max(0, min(v.offset, len(feed.lines)-rows))
	lines := make([]string, rows)
	v.feedSnippets = make([]liveActivitySnippet, rows)
	v.feedQuestions = make([]uint64, rows)
	for row := range rows {
		if index := v.offset + row; index < len(feed.lines) {
			lines[row], v.feedSnippets[row] = feed.lines[index], feed.snippets[index]
			v.feedQuestions[row] = feed.questions[index]
			if index == v.questionHover-1 && feed.questions[index] != 0 {
				lines[row] = underlineLink(lines[row])
			}
		}
	}
	// Pin only when the run keeps a visible line under its heading. Main's
	// transcript items do not always start with a heading, so it never pins.
	if !v.conversation && rows > 1 && v.offset+1 < len(feed.heads) && feed.heads[v.offset] != v.offset && feed.heads[v.offset+1] == feed.heads[v.offset] {
		lines[0], v.feedSnippets[0] = feed.lines[feed.heads[v.offset]], liveActivitySnippet{}
		v.feedQuestions[0] = 0
	}
	if target, ok := v.questionRows[v.flashQuestion]; ok && time.Now().Before(v.flashUntil) {
		for row := range lines {
			if index := v.offset + row; index < len(feed.heads) && feed.heads[index] == target {
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

func (v *liveActivityView) footer(width int) string {
	mode, toggle := "ALL", "o only"
	if v.only {
		mode, toggle = "ONLY", "o all"
	}
	keys := "n/p agent · " + toggle + " · j/k scroll · r follow"
	if !v.feedOnly {
		keys = "click agent · " + keys
	}

	if width < 60 {
		keys = "n/p · o · j/k · r"
	}
	return ansi.Truncate("\x1b[1m"+mode+liveActivityUndim+liveActivityDim+" · "+keys+liveActivityUndim, width, "…")
}

func liveActivityAge(age time.Duration) string {
	switch {
	case age < 2*time.Second:
		return "now"
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	}
	return fmt.Sprintf("%dh", int(age.Hours()))
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
	rows := v.roster()
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
	case 'o':
		v.only = !v.only
		v.follow()
	}
	return "", false
}
