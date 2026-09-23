package router

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const (
	liveActivityFeedLimit = 2000
	liveActivityEntryRows = 6 // Per-entry clamp in the interleaved feed.
)

// The agents view is presentation state only. Entries carry router sequence
// numbers, so a reconnect snapshot merges without duplicating retained rows.
type liveActivityView struct {
	agents    []activityPaneAgent
	entries   []activityPaneEntry
	lastSeq   uint64
	selected  string
	only      bool
	following bool
	offset    int
	unseen    int
	status    string

	// Geometry of the last frame, used by scrolling keys.
	feedLines, feedRows int
}

type liveActivityRosterRow struct {
	agent activityPaneAgent
	depth int
	color string
}

func newLiveActivityView() *liveActivityView {
	return &liveActivityView{following: true, status: "CONNECTING"}
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
		if !v.following && v.visible(entry) {
			v.unseen++
		}
	}
	if extra := len(v.entries) - liveActivityFeedLimit; extra > 0 {
		v.entries = slices.Delete(v.entries, 0, extra)
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
	for i := range rows {
		rows[i].color = v.color(rows[i].agent.Name)
	}
	return rows
}

// color matches the live diff pane's attribution for the same agent.
func (v *liveActivityView) color(name string) string {
	if color := liveAgentColor(name); color != "" {
		return color
	}
	return "\x1b[1m"
}

func (v *liveActivityView) latest(name string) (activityPaneEntry, bool) {
	for i := len(v.entries) - 1; i >= 0; i-- {
		if v.entries[i].Agent == name {
			return v.entries[i], true
		}
	}
	return activityPaneEntry{}, false
}

// glyph shows only observed facts: an open provider response, a latest error
// event, or a sent plaintext final answer. None of them claims completion.
func (v *liveActivityView) glyph(agent activityPaneAgent) string {
	latest, _ := v.latest(agent.Name)
	switch {
	case agent.Responding:
		return "◐"
	case latest.Kind == "error":
		return "!"
	case agent.Final:
		return "✓"
	}
	return "·"
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

func (v *liveActivityView) render(width, height int, now time.Time) []string {
	width, height = max(10, width), max(3, height)
	text := max(1, width-1)
	rows := v.roster()
	body := height - 2
	var roster []string
	// The roster needs its line, a separator, and at least one feed row.
	if len(rows) >= 2 && body >= 3 {
		roster = v.renderRoster(rows, text, body, now)
	}
	feedRows := body
	if len(roster) > 0 {
		feedRows -= len(roster) + 1
	}
	feed := v.renderFeed(rows, text)
	v.feedLines, v.feedRows = len(feed), max(0, feedRows)
	if v.following {
		v.offset = max(0, len(feed)-v.feedRows)
		v.unseen = 0
	}
	v.offset = max(0, min(v.offset, len(feed)-v.feedRows))

	lines := []string{v.header(rows, text)}
	lines = append(lines, roster...)
	if len(roster) > 0 {
		lines = append(lines, "\x1b[2m"+strings.Repeat("─", text)+"\x1b[0m")
	}
	for row := range v.feedRows {
		line := ""
		if index := v.offset + row; index < len(feed) {
			line = feed[index]
		}
		lines = append(lines, line)
	}
	lines = append(lines, ansi.Truncate(v.footer(), text, "…"))
	return lines
}

func (v *liveActivityView) header(rows []liveActivityRosterRow, width int) string {
	left := "AGENTS"
	responding := 0
	for _, row := range rows {
		if row.agent.Responding {
			responding++
		}
	}
	switch {
	case len(rows) == 0:
		left += " · waiting for subagent activity"
	case v.only:
		index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
		left += fmt.Sprintf(" · %s (%d/%d)", v.selected, index+1, len(rows))
	case len(rows) == 1:
		left += " · " + v.glyph(rows[0].agent) + " " + rows[0].agent.Name
	default:
		left += fmt.Sprintf(" · %d · %d responding", len(rows), responding)
	}
	right := "FOLLOW"
	if !v.following {
		right = "PAUSED"
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

func (v *liveActivityView) renderRoster(rows []liveActivityRosterRow, width, body int, now time.Time) []string {
	selected := max(0, slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected }))
	// Very short panes keep one roster line so the feed stays readable.
	if body < 8 {
		return []string{ansi.Truncate(fmt.Sprintf("%d agents · %s · n/p select", len(rows), v.selected), width, "…")}
	}
	limit := max(3, body/3)
	start, shown, more := 0, len(rows), 0
	if len(rows) > limit {
		shown = limit - 1
		start = max(0, min(selected-shown+1, len(rows)-shown))
		start = min(start, selected)
		more = len(rows) - shown
	}
	var lines []string
	for i, row := range rows[start : start+shown] {
		marker := " "
		if start+i == selected {
			marker = "▸"
		}
		name := strings.Repeat("  ", row.depth) + row.agent.Name
		summary, age := "", ""
		if latest, ok := v.latest(row.agent.Name); ok {
			summary, age = liveActivitySummary(latest.Text), liveActivityAge(now.Sub(latest.Observed))
			// A Code Mode batch reports several operations in one entry.
			if more := liveActivityOperations(latest) - 1; more > 0 {
				summary += fmt.Sprintf(" · +%d more", more)
			}
		}
		nameWidth := min(ansi.StringWidth(name), max(8, width*2/5))
		name = liveActivityMiddle(name, nameWidth)
		left := marker + v.glyph(row.agent) + " " + row.color + name + "\x1b[0m"
		used := 3 + nameWidth + 2
		ageWidth := ansi.StringWidth(age)
		summaryWidth := max(0, width-used-ageWidth-1)
		summary = ansi.Truncate(summary, summaryWidth, "…")
		pad := max(0, width-used-ansi.StringWidth(summary)-ageWidth)
		lines = append(lines, left+"  "+summary+strings.Repeat(" ", pad)+"\x1b[2m"+age+"\x1b[0m")
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("\x1b[2m  +%d more · n/p to scroll\x1b[0m", more))
	}
	return lines
}

func (v *liveActivityView) renderFeed(rows []liveActivityRosterRow, width int) []string {
	colors := make(map[string]string, len(rows))
	for _, row := range rows {
		colors[row.agent.Name] = row.color
	}
	var lines []string
	previous := ""
	for _, entry := range v.entries {
		if !v.visible(entry) {
			continue
		}
		if entry.Agent != previous {
			color := colors[entry.Agent]
			if color == "" {
				color = "\x1b[1m"
			}
			lines = append(lines, ansi.Truncate(color+entry.Agent+"\x1b[0m\x1b[2m · "+entry.Observed.Local().Format("15:04:05")+"\x1b[0m", width, "…"))
			previous = entry.Agent
		}
		body := liveActivityText(entry.Text, width-2)
		if !v.only && len(body) > liveActivityEntryRows {
			hidden := len(body) - liveActivityEntryRows + 1
			body = append(body[:liveActivityEntryRows-1], fmt.Sprintf("\x1b[2m… +%d lines · o shows this agent in full\x1b[0m", hidden))
		}
		for _, line := range body {
			lines = append(lines, "  "+line)
		}
	}
	return lines
}

func (v *liveActivityView) footer() string {
	if v.only {
		return "ONLY " + v.selected + " · n/p agent · o all · j/k · r follow · q quit"
	}
	return "ALL · n/p agent · o only · j/k · r follow · q quit"
}

// liveActivityText renders the commentary Markdown subset used by activity:
// fenced programs keep their lines under a gutter, and inline code or bold
// markers become terminal emphasis. Nothing is interpreted or executed.
func liveActivityText(text string, width int) []string {
	width = max(4, width)
	var lines []string
	fence := ""
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\t", "    "), "\n") {
		if fence != "" {
			if line == fence {
				fence = ""
				continue
			}
			for _, part := range strings.Split(ansi.Hardwrap(line, width-2, true), "\n") {
				lines = append(lines, "\x1b[2m│\x1b[0m "+part)
			}
			continue
		}
		if delimiter, ok := toolActivityFenceDelimiter(line); ok {
			fence = delimiter
			continue
		}
		for _, part := range strings.Split(ansi.Wrap(liveActivityInline(line), width, ""), "\n") {
			lines = append(lines, part)
		}
	}
	for len(lines) > 0 && ansi.Strip(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func liveActivityInline(line string) string {
	var out strings.Builder
	bold := false
	for i := 0; i < len(line); {
		switch {
		case line[i] == '`':
			run := 1
			for i+run < len(line) && line[i+run] == '`' {
				run++
			}
			fence := strings.Repeat("`", run)
			end := strings.Index(line[i+run:], fence)
			if end < 0 {
				out.WriteString(line[i : i+run])
				i += run
				continue
			}
			code := line[i+run : i+run+end]
			if len(code) > 1 && code[0] == ' ' && code[len(code)-1] == ' ' {
				code = code[1 : len(code)-1]
			}
			out.WriteString("\x1b[1m" + ansi.Strip(code) + "\x1b[22m")
			i += run + end + run
		case strings.HasPrefix(line[i:], "**"):
			bold = !bold
			if bold {
				out.WriteString("\x1b[1m")
			} else {
				out.WriteString("\x1b[22m")
			}
			i += 2
		default:
			out.WriteByte(line[i])
			i++
		}
	}
	if bold {
		out.WriteString("\x1b[22m")
	}
	return out.String()
}

// liveActivitySummary is the first readable line of an entry, without markup.
func liveActivitySummary(text string) string {
	for _, line := range liveActivityText(text, 1<<16) {
		if plain := strings.TrimSpace(strings.TrimPrefix(ansi.Strip(line), "│")); plain != "" {
			return plain
		}
	}
	return ""
}

// liveActivityOperations counts the blank-line separated operations of a tool
// entry. Blank lines inside fenced programs do not start a new operation.
func liveActivityOperations(entry activityPaneEntry) int {
	if entry.Kind != "tool" {
		return 1
	}
	count, fence, blank := 0, "", true
	for line := range strings.SplitSeq(entry.Text, "\n") {
		if fence != "" {
			if line == fence {
				fence = ""
			}
			continue
		}
		if delimiter, ok := toolActivityFenceDelimiter(line); ok {
			fence = delimiter
			blank = false
			continue
		}
		if strings.TrimSpace(line) == "" {
			blank = true
			continue
		}
		if blank {
			count++
		}
		blank = false
	}
	return max(1, count)
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
