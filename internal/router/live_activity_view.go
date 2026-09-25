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

// The agents view is presentation state only. Entries carry router sequence
// numbers, so a reconnect snapshot merges without duplicating retained rows.
type liveActivityView struct {
	agents    []activityPaneAgent
	entries   []activityPaneEntry
	blocks    [][]liveActivityBlock // Parsed entries, aligned with entries.
	lastSeq   uint64
	selected  string
	hovered   string
	only      bool
	hits      []liveActivityHit
	following bool
	offset    int
	unseen    int
	status    string
	feedOnly  bool
	painter   liveActivityPainter
	osc       livediff.OSC
	runs      map[liveActivityRunKey]liveActivityRun

	// expanded snippets show in full in the shared feed; snippet is the
	// hovered collapsed one.
	expanded map[liveActivitySnippet]bool
	snippet  liveActivitySnippet

	// Geometry of the last frame, used by scrolling keys and the pointer.
	// feedSnippets holds each feed row's snippet, from screen row feedTop
	// between columns feedLeft and feedRight.
	feedLines, feedRows          int
	feedSnippets                 []liveActivitySnippet
	feedTop, feedLeft, feedRight int
	width, height                int
}

// liveActivitySnippet names a clippable block in the shared feed: the
// sequence of its run's first entry and its index in the run. Sequences start
// at one, so the zero value names no snippet.
type liveActivitySnippet struct {
	run   uint64
	block int
}

type liveActivityRun struct {
	lines    []string
	snippets []liveActivitySnippet // Aligned with lines.
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
		v.agents = event.Agents
	}
	for _, entry := range event.Entries {
		if entry.Seq <= v.lastSeq {
			continue
		}
		v.lastSeq = entry.Seq
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
						if v.blocks[i][j].verb == "Run" {
							v.blocks[i][j].exitCode, _ = strconv.Atoi(entry.Text)
						}
					}
					v.runs = nil
					break
				}
			}
			continue
		}
		v.entries = append(v.entries, entry)
		v.blocks = append(v.blocks, parseLiveActivity(entry))
		if !v.following && v.visible(entry) {
			v.unseen++
		}
	}
	if extra := len(v.entries) - liveActivityFeedLimit; extra > 0 {
		v.entries = slices.Delete(v.entries, 0, extra)
		v.blocks = slices.Delete(v.blocks, 0, extra)
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
	latest := v.latest(agent.Name)
	switch {
	case agent.Responding:
		return liveActivityAmber + "◐" + liveActivityReset
	case latest >= 0 && v.entries[latest].Kind == "error":
		return liveActivityRed + "!" + liveActivityReset
	case agent.Final:
		return liveActivityGreen + "✓" + liveActivityReset
	}
	return liveActivityDim + "·" + liveActivityUndim
}

func (v *liveActivityView) selectAgent(step int) {
	rows := v.roster()
	if len(rows) == 0 {
		return
	}
	index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
	index = (max(0, index) + step + len(rows)) % len(rows)
	v.selected = rows[index].agent.Name
	v.hovered = ""
	if v.only {
		v.follow()
	}
}

func (v *liveActivityView) handleMouse(action byte, row, column int) bool {
	if action == 'j' || action == 'k' {
		return v.scrollKey(paneWheelKey(action))
	}

	if action != 'h' && action != '\r' {
		return false
	}
	snippet := v.pointSnippet(action, row, column)
	agent := false
	if !v.feedOnly {
		agent = v.pointAgent(action, row, column)
	}
	return snippet || agent
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
		v.offset = next
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
	v.offset = max(0, min(v.offset+delta, v.feedLines-1))
}

// render lays the pane out for its size: agent cards beside the feed on wide
// panes, a roster above the feed on narrower ones, and a one-line strip when
// rows are scarce. Every row fits within width-1 columns.
func (v *liveActivityView) render(width, height int, now time.Time) []string {
	width, height = max(10, width), max(3, height)
	if width != v.width || height != v.height {
		v.hovered, v.snippet = "", liveActivitySnippet{}
		v.width, v.height = width, height
	}
	v.hits = v.hits[:0]
	v.feedSnippets = nil
	text := max(1, width-1)
	rows := v.roster()
	footer := height >= 10
	body := height - 1
	if footer {
		body--
	}
	lines := []string{v.header(rows, text)}
	v.feedTop, v.feedLeft, v.feedRight = 2, 1, text
	switch {
	case v.feedOnly:
		lines = append(lines, v.viewport(v.renderFeed(text, body), body)...)
	case len(rows) > 0 && text >= liveActivitySideColumns && body >= 6:
		cardWidth := min(44, max(28, text*3/10))
		feedWidth := text - cardWidth - 3
		cards := v.renderCards(rows, cardWidth, body, now)
		feed := v.viewport(v.renderFeed(feedWidth, body), body)
		v.feedLeft = cardWidth + 4
		for i := range body {
			lines = append(lines, liveActivityPad(cards[i], cardWidth)+liveActivityDim+" │ "+liveActivityUndim+feed[i])
		}
	case len(rows) > 0 && body >= 8:
		roster := v.renderRoster(rows, text, max(2, body/3), now)
		feedRows := body - len(roster) - 1
		lines = append(lines, roster...)
		lines = append(lines, liveActivityDim+strings.Repeat("─", text)+liveActivityUndim)
		v.feedTop = len(lines) + 1
		lines = append(lines, v.viewport(v.renderFeed(text, feedRows), feedRows)...)
	case len(rows) > 0:
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
	title := "AGENTS"

	left := "\x1b[1m" + v.painter.theme.Accent() + title + liveActivityReset
	responding := 0
	for _, row := range rows {
		if row.agent.Responding {
			responding++
		}
	}
	switch {
	case len(rows) == 0:
		left += liveActivityDim + " · waiting for subagent activity" + liveActivityUndim
	case v.only:
		index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
		left += fmt.Sprintf(" · only %s %s(%d/%d)%s", v.painter.agent(v.selected), liveActivityDim, index+1, len(rows), liveActivityUndim)
	default:
		left += fmt.Sprintf(" · %d · %d responding", len(rows), responding)
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
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 2 {
		return ansi.Truncate(left, max(0, width-ansi.StringWidth(right)-1), "…") + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func liveActivityDisplayName(name string) string {
	if name == "/root" {
		return "main"
	}
	_, leaf, found := strings.CutLast(name, "/")
	if found {
		return leaf
	}
	return name
}

// liveActivityRosterName drops the shared /root/ prefix; nested agents show
// their leaf under the parent. Feed headings always carry the full path.
func liveActivityRosterName(row liveActivityRosterRow) string {
	if row.agent.Name == "/root" {
		return "main"
	}
	name := strings.TrimPrefix(row.agent.Name, "/root/")
	if row.depth > 0 {
		name = strings.Repeat("  ", row.depth-1) + "└ " + name[strings.LastIndex(name, "/")+1:]
	}
	return name
}

// current is an agent's latest activity summary and its elapsed/response timer.
func (v *liveActivityView) current(agent activityPaneAgent, now time.Time) (string, string) {
	summary := liveActivityDim + "no activity yet" + liveActivityUndim
	if latest := v.latest(agent.Name); latest >= 0 {
		summary = v.painter.summary(v.blocks[latest])
	}
	if agent.Started.IsZero() {
		if latest := v.latest(agent.Name); latest >= 0 {
			return summary, liveActivityAge(max(0, now.Sub(v.entries[latest].Observed))) + " · —"
		}
		return summary, ""
	}
	last := "—"
	if !agent.LastResponse.IsZero() {
		last = liveActivityAge(max(0, now.Sub(agent.LastResponse))) + " ago"
	}
	return summary, liveActivityAge(max(0, now.Sub(agent.Started))) + " · " + last
}

// liveActivityTokens shows cumulative input (sent) and output (received) tokens.
func liveActivityTokens(agent activityPaneAgent) string {
	if agent.InputTokens == 0 && agent.OutputTokens == 0 {
		return ""
	}
	return "↑ " + formatUsageTokens(agent.InputTokens) + " ↓ " + formatUsageTokens(agent.OutputTokens)
}

func liveActivityCost(agent activityPaneAgent) string {
	if agent.Turns == 0 {
		return ""
	}
	cost := "n/a"
	if agent.CostKnown {
		cost = fmt.Sprintf("$%.4f", agent.Cost)
	}
	return cost
}

func liveActivityTurns(agent activityPaneAgent) string {
	if agent.Turns == 0 {
		return ""
	}
	return fmt.Sprint(agent.Turns) + " turns"
}

// liveActivityWindow keeps the selected item visible among at most limit items.
func liveActivityWindow(count, selected, limit int) (start, end int) {
	if count <= limit {
		return 0, count
	}
	start = max(0, min(selected-limit/2, count-limit))
	return start, start + limit
}

func (v *liveActivityView) marker(selected bool, hovered bool) string {
	if selected {
		return v.painter.theme.Accent() + "▸" + liveActivityReset
	}
	if hovered {
		return v.painter.theme.Accent() + "▹" + liveActivityReset
	}
	return " "
}

func (v *liveActivityView) hoverName(name, agent string) string {
	if agent == v.hovered {
		return "\x1b[4m" + name + "\x1b[24m"
	}
	return name
}

// renderCards uses the same two-line agent layout beside the feed.
func (v *liveActivityView) renderCards(rows []liveActivityRosterRow, width, height int, now time.Time) []string {
	lines := v.renderRoster(rows, width, height, now)
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

// renderRoster keeps activity separate from secondary metadata and usage.
func (v *liveActivityView) renderRoster(rows []liveActivityRosterRow, width, limit int, now time.Time) []string {
	selected := max(0, slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected }))
	capacity := max(1, limit/2)
	if len(rows) > capacity && limit > 2 {
		capacity = max(1, (limit-1)/2)
	}
	start, end := liveActivityWindow(len(rows), selected, capacity)
	nameWidth := 0
	for _, row := range rows {
		nameWidth = max(nameWidth, ansi.StringWidth(liveActivityRosterName(row)))
	}
	nameWidth = max(1, min(nameWidth, width/3))
	var lines []string
	for i := start; i < end; i++ {
		row := rows[i]
		first := len(lines) + 2
		summary, timer := v.current(row.agent, now)
		name := liveAgentColor(row.agent.Name) + v.hoverName(liveActivityMiddle(liveActivityRosterName(row), nameWidth), row.agent.Name) + liveActivityReset
		line := v.marker(i == selected, row.agent.Name == v.hovered) + v.glyph(row.agent) + " " + name + "  " + summary
		lines = append(lines, ansi.Truncate(line, width, "…"))
		if len(lines) < limit {
			metric := liveActivityMetrics(row.agent, timer)
			lines = append(lines, ansi.Truncate("   "+liveActivityDim+metric+liveActivityUndim, width, "…"))
		}
		for hit := first; hit < len(lines)+2; hit++ {
			v.hits = append(v.hits, liveActivityHit{hit, 1, width, row.agent.Name})
		}
	}
	if hidden := len(rows) - (end - start); hidden > 0 && len(lines) < limit {
		lines = append(lines, ansi.Truncate(liveActivityDim+fmt.Sprintf("  +%d more · n/p", hidden)+liveActivityUndim, width, "…"))
	}
	return lines
}

func liveActivityMetrics(agent activityPaneAgent, timer string) string {
	var parts []string
	for _, part := range []string{agent.Configuration, agent.Role, timer, liveActivityTokens(agent), liveActivityCost(agent), liveActivityTurns(agent)} {
		if part != "" {
			parts = append(parts, strings.Join(strings.Fields(livediff.Safe(part, false)), " "))
		}
	}
	return strings.Join(parts, " · ")
}

// renderStrip is the one-line roster for short panes.
func (v *liveActivityView) renderStrip(rows []liveActivityRosterRow, width int) string {
	var parts []string
	column := 1
	for _, row := range rows {
		name := strings.TrimPrefix(row.agent.Name, "/root/")
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
	lines    []string
	heads    []int                 // Index of the heading that owns each line.
	snippets []liveActivitySnippet // Snippet that owns each line, if any.
}

// renderFeed groups consecutive entries of one agent under a heading with a
// colored gutter. Adjacent reads collapse into one row. In the interleaved
// view each block is clipped to a share of the feed that grows with the pane,
// unless the viewer expanded it.
func (v *liveActivityView) renderFeed(width, rows int) liveActivityFeed {
	clip := 0
	if !v.only {
		clip = min(12, max(3, rows/3))
	}
	var feed liveActivityFeed
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
		key := liveActivityRunKey{v.entries[i].Seq, v.entries[last].Seq, width, clip, v.painter.theme, -1}
		if v.snippet.run == key.first {
			key.hover = v.snippet.block
		}
		run, ok := v.runs[key]
		if !ok {
			var blocks []liveActivityBlock
			for k := i; k <= last; k++ {
				if v.visible(v.entries[k]) {
					blocks = append(blocks, v.blocks[k]...)
				}
			}
			run = v.renderRun(key.first, agent, v.entries[last].Observed, mergeLiveActivityReads(blocks), width, clip)
		}
		used[key] = run
		head := len(feed.lines)
		for range run.lines {
			feed.heads = append(feed.heads, head)
		}
		feed.lines = append(feed.lines, run.lines...)
		feed.snippets = append(feed.snippets, run.snippets...)
		i = j
	}
	v.runs = used
	return feed
}

func (v *liveActivityView) renderRun(first uint64, agent string, observed time.Time, blocks []liveActivityBlock, width, clip int) liveActivityRun {
	stamp := " " + observed.Local().Format("15:04:05")
	head := liveAgentGutter(agent, v.painter.theme) + "●" + liveActivityReset + " " + v.painter.agent(agent)
	rule := max(1, width-ansi.StringWidth(head)-ansi.StringWidth(stamp)-1)
	run := liveActivityRun{
		lines:    []string{ansi.Truncate(head+" "+liveActivityDim+strings.Repeat("─", rule)+stamp+liveActivityUndim, width, "")},
		snippets: make([]liveActivitySnippet, 1),
	}
	gutter := liveAgentGutter(agent, v.painter.theme) + "▎" + liveActivityReset + " "
	for index, block := range blocks {
		block.compact = clip > 0
		part := v.painter.block(block, width-2)
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
		for _, line := range part {
			run.lines = append(run.lines, gutter+ansi.Truncate(line, width-2, "…"))
			run.snippets = append(run.snippets, snippet)
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
	v.offset = max(0, min(v.offset, len(feed.lines)-1))
	lines := make([]string, rows)
	v.feedSnippets = make([]liveActivitySnippet, rows)
	for row := range rows {
		if index := v.offset + row; index < len(feed.lines) {
			lines[row], v.feedSnippets[row] = feed.lines[index], feed.snippets[index]
		}
	}
	// Pin only when the run keeps a visible line under its heading.
	if rows > 1 && v.offset+1 < len(feed.heads) && feed.heads[v.offset] != v.offset && feed.heads[v.offset+1] == feed.heads[v.offset] {
		lines[0], v.feedSnippets[0] = feed.lines[feed.heads[v.offset]], liveActivitySnippet{}
	}
	return lines
}

func (v *liveActivityView) footer(width int) string {
	mode, toggle := "ALL", "o only"
	if v.only {
		mode, toggle = "ONLY", "o all"
	}
	keys := "click agent · n/p agent · " + toggle + " · j/k scroll · r follow"

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

// renderRosterPane shares selection with the feed without duplicating its state.
func (v *liveActivityView) renderRosterPane(width, height int, now time.Time) []string {
	width, height = max(1, width-1), max(1, height)
	v.hits = v.hits[:0]
	rows := v.roster()
	lines := []string{v.header(rows, width)}
	if height > 1 && len(rows) > 0 {
		lines = append(lines, v.renderRoster(rows, width, height-1, now)...)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
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
