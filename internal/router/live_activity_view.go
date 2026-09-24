package router

import (
	"fmt"
	"os"
	"slices"
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
	only      bool
	following bool
	offset    int
	unseen    int
	status    string
	painter   liveActivityPainter
	osc       livediff.OSC
	runs      map[liveActivityRunKey][]string

	// Geometry of the last frame, used by scrolling keys.
	feedLines, feedRows int
}

type liveActivityRosterRow struct {
	agent activityPaneAgent
	depth int
}

type liveActivityRunKey struct {
	first, last uint64
	width, clip int
	theme       livediff.Theme
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
		v.agents = event.Agents
	}
	for _, entry := range event.Entries {
		if entry.Seq <= v.lastSeq {
			continue
		}
		v.lastSeq = entry.Seq
		v.entries = append(v.entries, entry)
		v.blocks = append(v.blocks, parseLiveActivity(entry))
		if !v.following && v.visible(entry) {
			v.unseen++
		}
	}
	if extra := len(v.entries) - liveActivityFeedLimit; extra > 0 {
		v.entries = slices.Delete(v.entries, 0, extra)
		v.blocks = slices.Delete(v.blocks, 0, extra)
	}
	if v.selected == "" || !slices.ContainsFunc(v.agents, func(a activityPaneAgent) bool { return a.Name == v.selected }) {
		if rows := v.roster(); len(rows) > 0 {
			v.selected = rows[0].agent.Name
		}
	}
	return false
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
	if v.only {
		v.follow()
	}
}

func (v *liveActivityView) follow() {
	v.following, v.unseen = true, 0
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
	width, height = max(10, width), max(3, height)
	text := max(1, width-1)
	rows := v.roster()
	footer := height >= 10
	body := height - 1
	if footer {
		body--
	}
	lines := []string{v.header(rows, text)}
	switch {
	case len(rows) > 0 && text >= liveActivitySideColumns && body >= 6:
		cardWidth := min(44, max(28, text*3/10))
		feedWidth := text - cardWidth - 3
		cards := v.renderCards(rows, cardWidth, body, now)
		feed := v.viewport(v.renderFeed(feedWidth, body), body)
		for i := range body {
			lines = append(lines, liveActivityPad(cards[i], cardWidth)+liveActivityDim+" │ "+liveActivityUndim+feed[i])
		}
	case len(rows) > 0 && body >= 8:
		roster := v.renderRoster(rows, text, max(2, body/3), now)
		feedRows := body - len(roster) - 1
		lines = append(lines, roster...)
		lines = append(lines, liveActivityDim+strings.Repeat("─", text)+liveActivityUndim)
		lines = append(lines, v.viewport(v.renderFeed(text, feedRows), feedRows)...)
	case len(rows) > 0:
		lines = append(lines, v.renderStrip(rows, text))
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
	left := "\x1b[1m" + v.painter.theme.Accent() + "AGENTS" + liveActivityReset
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

// liveActivityRosterName drops the shared /root/ prefix; nested agents show
// their leaf under the parent. Feed headings always carry the full path.
func liveActivityRosterName(row liveActivityRosterRow) string {
	name := strings.TrimPrefix(row.agent.Name, "/root/")
	if row.depth > 0 {
		name = strings.Repeat("  ", row.depth-1) + "└ " + name[strings.LastIndex(name, "/")+1:]
	}
	return name
}

// current is an agent's latest activity summary and its age.
func (v *liveActivityView) current(name string, now time.Time) (string, string) {
	latest := v.latest(name)
	if latest < 0 {
		return liveActivityDim + "no activity yet" + liveActivityUndim, ""
	}
	return v.painter.summary(v.blocks[latest]), liveActivityAge(now.Sub(v.entries[latest].Observed))
}

// liveActivityWindow keeps the selected item visible among at most limit items.
func liveActivityWindow(count, selected, limit int) (start, end int) {
	if count <= limit {
		return 0, count
	}
	start = max(0, min(selected-limit/2, count-limit))
	return start, start + limit
}

func (v *liveActivityView) marker(selected bool) string {
	if selected {
		return v.painter.theme.Accent() + "▸" + liveActivityReset
	}
	return " "
}

// renderCards shows each agent as a name row and a current-activity row. A
// short pane keeps one row per agent, and the selected agent keeps its detail.
func (v *liveActivityView) renderCards(rows []liveActivityRosterRow, width, height int, now time.Time) []string {
	selected := max(0, slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected }))
	detailed := len(rows)*2 <= height
	limit := height / 2
	if !detailed {
		// Reserve the selected agent's detail row and, if needed, the overflow line.
		limit = height - 1
		if len(rows) > limit {
			limit--
		}
	}
	start, end := liveActivityWindow(len(rows), selected, max(1, limit))
	var lines []string
	for i := start; i < end; i++ {
		row := rows[i]
		summary, age := v.current(row.agent.Name, now)
		name := ansi.Truncate(liveAgentColor(row.agent.Name)+liveActivityRosterName(row)+liveActivityReset, max(1, width-4-ansi.StringWidth(age)), "…")
		gap := max(1, width-3-ansi.StringWidth(name)-ansi.StringWidth(age))
		lines = append(lines, v.marker(i == selected)+v.glyph(row.agent)+" "+name+strings.Repeat(" ", gap)+liveActivityDim+age+liveActivityUndim)
		if detailed || i == selected {
			lines = append(lines, "   "+ansi.Truncate(summary, width-3, "…"))
		}
	}
	if hidden := len(rows) - (end - start); hidden > 0 {
		lines = append(lines, liveActivityDim+fmt.Sprintf("   +%d more · n/p", hidden)+liveActivityUndim)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// renderRoster shows one row per agent: glyph, name, current activity, age.
func (v *liveActivityView) renderRoster(rows []liveActivityRosterRow, width, limit int, now time.Time) []string {
	selected := max(0, slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected }))
	if len(rows) > limit {
		limit--
	}
	start, end := liveActivityWindow(len(rows), selected, max(1, limit))
	nameWidth := 0
	for _, row := range rows {
		nameWidth = max(nameWidth, ansi.StringWidth(liveActivityRosterName(row)))
	}
	nameWidth = max(1, min(nameWidth, width-10-max(8, width/4)))
	var lines []string
	for i := start; i < end; i++ {
		row := rows[i]
		summary, age := v.current(row.agent.Name, now)
		name := liveAgentColor(row.agent.Name) + liveActivityMiddle(liveActivityRosterName(row), nameWidth) + liveActivityReset
		summaryWidth := max(0, width-3-nameWidth-2-ansi.StringWidth(age)-1)
		summary = ansi.Truncate(summary, summaryWidth, "…")
		pad := max(1, width-3-nameWidth-2-ansi.StringWidth(summary)-ansi.StringWidth(age))
		line := v.marker(i == selected) + v.glyph(row.agent) + " " + name + "  " + summary + strings.Repeat(" ", pad) + liveActivityDim + age + liveActivityUndim
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	if hidden := len(rows) - (end - start); hidden > 0 {
		lines = append(lines, ansi.Truncate(liveActivityDim+fmt.Sprintf("  +%d more · n/p", hidden)+liveActivityUndim, width, "…"))
	}
	return lines
}

// renderStrip is the one-line roster for short panes.
func (v *liveActivityView) renderStrip(rows []liveActivityRosterRow, width int) string {
	var parts []string
	for _, row := range rows {
		name := strings.TrimPrefix(row.agent.Name, "/root/")
		if row.agent.Name == v.selected {
			name = "\x1b[4m" + name + "\x1b[24m"
		}
		parts = append(parts, v.glyph(row.agent)+" "+liveAgentColor(row.agent.Name)+name+liveActivityReset)
	}
	return ansi.Truncate(strings.Join(parts, "  "), width, "…")
}

type liveActivityFeed struct {
	lines []string
	heads []int // Index of the heading that owns each line.
}

// renderFeed groups consecutive entries of one agent under a heading with a
// colored gutter. Adjacent reads collapse into one row. In the interleaved
// view each block is clipped to a share of the feed that grows with the pane.
func (v *liveActivityView) renderFeed(width, rows int) liveActivityFeed {
	clip := 0
	if !v.only {
		clip = min(12, max(3, rows/3))
	}
	var feed liveActivityFeed
	used := make(map[liveActivityRunKey][]string)
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
		key := liveActivityRunKey{v.entries[i].Seq, v.entries[last].Seq, width, clip, v.painter.theme}
		lines, ok := v.runs[key]
		if !ok {
			var blocks []liveActivityBlock
			for k := i; k <= last; k++ {
				if v.visible(v.entries[k]) {
					blocks = append(blocks, v.blocks[k]...)
				}
			}
			lines = v.renderRun(agent, v.entries[last].Observed, mergeLiveActivityReads(blocks), width, clip)
		}
		used[key] = lines
		head := len(feed.lines)
		for range lines {
			feed.heads = append(feed.heads, head)
		}
		feed.lines = append(feed.lines, lines...)
		i = j
	}
	v.runs = used
	return feed
}

func (v *liveActivityView) renderRun(agent string, observed time.Time, blocks []liveActivityBlock, width, clip int) []string {
	stamp := " " + observed.Local().Format("15:04:05")
	head := liveAgentGutter(agent, v.painter.theme) + "●" + liveActivityReset + " " + v.painter.agent(agent)
	rule := max(1, width-ansi.StringWidth(head)-ansi.StringWidth(stamp)-1)
	lines := []string{ansi.Truncate(head+" "+liveActivityDim+strings.Repeat("─", rule)+stamp+liveActivityUndim, width, "")}
	gutter := liveAgentGutter(agent, v.painter.theme) + "▎" + liveActivityReset + " "
	for _, block := range blocks {
		block.compact = clip > 0
		part := v.painter.block(block, width-2)
		// Messages carry results, so they get twice the operation share.
		limit := clip
		if block.kind == "message" || block.kind == "final" {
			limit *= 2
		}
		if limit > 0 && len(part) > limit {
			hidden := len(part) - limit + 1
			part = append(part[:limit-1:limit-1], liveActivityDim+fmt.Sprintf("… +%d lines · o", hidden)+liveActivityUndim)
		}
		for _, line := range part {
			lines = append(lines, gutter+ansi.Truncate(line, width-2, "…"))
		}
	}
	return lines
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
	for row := range rows {
		if index := v.offset + row; index < len(feed.lines) {
			lines[row] = feed.lines[index]
		}
	}
	// Pin only when the run keeps a visible line under its heading.
	if rows > 1 && v.offset+1 < len(feed.heads) && feed.heads[v.offset] != v.offset && feed.heads[v.offset+1] == feed.heads[v.offset] {
		lines[0] = feed.lines[feed.heads[v.offset]]
	}
	return lines
}

func (v *liveActivityView) footer(width int) string {
	mode, toggle := "ALL", "o only"
	if v.only {
		mode, toggle = "ONLY", "o all"
	}
	keys := "n/p agent · " + toggle + " · j/k scroll · r follow · q quit"
	if width < 60 {
		keys = "n/p · o · j/k · r · q"
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
