package router

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// nativeTitle names the Activity feed's filter and follow state for the
// shell's pane title.
func (v *liveActivityView) nativeTitle() (string, string) {
	detail := ""
	if v.only {
		rows := v.roster()
		index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
		detail = v.painter.agent(v.selected) + liveActivityDim + fmt.Sprintf(" %d/%d", index+1, len(rows)) + liveActivityUndim
	}
	state := v.status
	if state == "" {
		state = scrollLabel(v)
		if v.following {
			state = strings.TrimSpace(state + " " + liveActivityDim + "FOLLOW" + liveActivityUndim)
		}
	}
	return detail, state
}

// nativeRosterItem is one roster row: an agent, or the fold of finished ones.
type nativeRosterItem struct {
	row      liveActivityRosterRow
	finished []liveActivityRosterRow
}

// nativeRoster fits the roster to its content within limit rows, under a rule
// carrying the counts and session totals. Unfocused, finished agents fold
// into one row. Rows show role and state, then timer, tokens, cost and turns;
// turns drop first when narrow.
func (v *liveActivityView) nativeRoster(width, limit int, now time.Time, focused bool) []string {
	rows := v.roster()
	if len(rows) == 0 || width < 20 {
		return nil
	}
	v.hits = v.hits[:0]
	var items []nativeRosterItem
	fold := -1
	for _, row := range rows {
		if !focused && row.agent.Name != "/root" && v.agentStatus(row.agent) == '✓' && !(v.only && row.agent.Name == v.selected) {
			if fold < 0 {
				fold = len(items)
				items = append(items, nativeRosterItem{})
			}
			items[fold].finished = append(items[fold].finished, row)
			continue
		}
		items = append(items, nativeRosterItem{row: row})
	}
	responding, errors := v.statusCounts(rows)
	var input, output uint64
	var cost float64
	costKnown, partial := false, false
	for _, row := range rows {
		input += row.agent.InputTokens
		output += row.agent.OutputTokens
		if row.agent.CostKnown {
			cost, costKnown = cost+row.agent.Cost, true
			partial = partial || row.agent.CostPartial
		} else if row.agent.Turns > 0 {
			partial = true
		}
	}
	detail := fmt.Sprintf("%d", len(rows))
	if responding > 0 {
		detail += liveActivityDim + " · " + liveActivityUndim + liveActivityAmber + fmt.Sprintf("%d working", responding) + liveActivityReset
	}
	if errors > 0 {
		detail += liveActivityDim + " · " + liveActivityUndim + liveActivityRed + fmt.Sprintf("%d error", errors) + liveActivityReset
	}
	var totals []string
	if input+output > 0 {
		totals = append(totals, "↑"+formatUsageTokens(input)+" ↓"+formatUsageTokens(output))
	}
	if costKnown {
		prefix := "$"
		if partial {
			prefix = "≥$"
		}
		totals = append(totals, prefix+fmt.Sprintf("%.2f", cost))
	}
	right := ""
	if len(totals) > 0 {
		right = liveActivityDim + strings.Join(totals, " · ") + liveActivityUndim
	}
	lines := []string{nativeRule("─", "─", "─", nativeTitle(4, "Agents", detail, focused), right, width, nativeBorder(focused))}

	limit = max(1, limit)
	selected := max(0, slices.IndexFunc(items, func(item nativeRosterItem) bool { return item.finished == nil && item.row.agent.Name == v.selected }))
	start := 0
	if len(items) > limit {
		start = max(0, min(selected-limit+2, len(items)-limit+1))
	}
	end := min(len(items), start+limit)
	if end < len(items) {
		end-- // Room for the hidden-count row.
	}
	nameWidth := 4
	for _, row := range rows {
		name, _ := rosterTree(rows, slices.IndexFunc(rows, func(other liveActivityRosterRow) bool { return other.agent.Name == row.agent.Name }), 0)
		nameWidth = max(nameWidth, ansi.StringWidth(name))
	}
	nameWidth = min(nameWidth, max(8, width/4))
	for _, item := range items[start:end] {
		if item.finished != nil {
			var names []string
			for _, row := range item.finished {
				names = append(names, liveAgentColor(row.agent.Name)+agentDisplayName(row.agent.Name)+liveActivityReset)
			}
			line := " " + liveActivityGreen + "✓" + liveActivityReset + " " + liveActivityDim + fmt.Sprintf("%d finished · ", len(item.finished)) + liveActivityUndim + strings.Join(names, liveActivityDim+", "+liveActivityUndim)
			lines = append(lines, ansi.Truncate(line, width, "…"))
			continue
		}
		row := item.row
		index := slices.IndexFunc(rows, func(other liveActivityRosterRow) bool { return other.agent.Name == row.agent.Name })
		tree, _ := rosterTree(rows, index, 0)
		tree = strings.TrimRight(liveActivityMiddle(tree, nameWidth), " ")
		split := strings.LastIndexAny(tree, " /") + 1
		color := liveAgentColor(row.agent.Name)
		if color == "" {
			color = "\x1b[1m" + v.painter.theme.Accent()
		}
		name := liveActivityDim + tree[:split] + liveActivityUndim + color + v.hoverName(tree[split:], row.agent.Name) + liveActivityReset
		line := " " + v.nativeGlyph(row.agent) + " " + name + strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(tree))) + "  "
		state := v.agentState(row.agent)
		if role := liveActivityRole(row.agent); role != "" {
			state = liveActivityDim + role + " · " + liveActivityUndim + state
		}
		metrics := nativeRosterMetrics(v, row.agent, now, width-ansi.StringWidth(line)-24)
		room := width - ansi.StringWidth(line) - ansi.StringWidth(metrics) - 1
		line += liveActivityPad(state, max(1, room)) + metrics
		line = ansi.Truncate(line, width, "…")
		if row.agent.Name == v.selected && (focused || v.only) {
			line = v.selectRow(line, width)
		}
		lines = append(lines, line)
		v.hits = append(v.hits, liveActivityHit{len(lines), 1, width, row.agent.Name})
	}
	if hidden := len(items) - end; hidden > 0 {
		lines = append(lines, liveActivityDim+fmt.Sprintf("   +%d more · ^B 4 shows all", hidden)+liveActivityUndim)
	}
	v.rosterOffset, v.rosterEnd = start, end
	return lines
}

// nativeRosterMetrics right-aligns timer, tokens, cost and turns within room,
// dropping turns first, then tokens, then the timer.
func nativeRosterMetrics(v *liveActivityView, agent activityPaneAgent, now time.Time, room int) string {
	_, timer := v.current(agent, now)
	if timer != "" {
		elapsed, last, _ := strings.Cut(timer, " · ")
		timer = liveActivityMetricValues(elapsed) + liveActivityDim + " · " + liveActivityUndim + liveActivityMetricValues(last)
	}
	tokens := ""
	if agent.InputTokens+agent.OutputTokens > 0 {
		tokens = liveActivityDim + "↑" + liveActivityUndim + formatUsageTokens(agent.InputTokens) + liveActivityDim + " ↓" + liveActivityUndim + formatUsageTokens(agent.OutputTokens)
	}
	cells := []struct {
		text  string
		width int
	}{{timer, 16}, {tokens, 14}, {liveActivityCost(agent), 7}, {liveActivityTurns(agent), 5}}
	for len(cells) > 0 {
		total := 0
		for _, cell := range cells {
			total += cell.width + 2
		}
		if total <= room {
			break
		}
		cells = cells[:len(cells)-1]
	}
	var b strings.Builder
	for _, cell := range cells {
		text := ansi.Truncate(cell.text, cell.width, "…")
		b.WriteString("  " + strings.Repeat(" ", cell.width-ansi.StringWidth(text)) + text)
	}
	return b.String()
}

// nativeGlyph shows the agent's state in the agent's own color, so an agent
// keeps one color in the roster, docks, Main and Activity.
func (v *liveActivityView) nativeGlyph(agent activityPaneAgent) string {
	color := liveAgentColor(agent.Name)
	if color == "" {
		color = v.painter.theme.Accent()
	}
	switch v.agentStatus(agent) {
	case '◐':
		return color + "◐" + liveActivityReset
	case '!':
		return liveActivityRed + "!" + liveActivityReset
	case '✓':
		return liveActivityGreen + "✓" + liveActivityReset
	}
	return color + "●" + liveActivityReset
}

// agentState preserves lifecycle states when idle, and shares the detailed
// activity summary while an agent is working.
func (v *liveActivityView) agentState(agent activityPaneAgent) string {
	source, owner := v, agent.Name
	if agent.Name == "/root" && v.mainView != nil {
		source, owner = v.mainView, "Main"
	}
	var blocks []liveActivityBlock
	var kind string
	for i, entry := range slices.Backward(source.entries) {
		if entry.Agent == owner || source == v && entry.Agent == agent.Name {
			blocks, kind = source.blocks[i], entry.Kind
			break
		}
	}
	switch {
	case kind == "error":
		return liveActivityRed + "error" + liveActivityReset
	case !agent.Responding && agent.Final:
		return liveActivityDim + "done" + liveActivityUndim
	case !agent.Responding:
		return liveActivityDim + "idle" + liveActivityUndim
	case len(blocks) == 0:
		return "working"
	}
	summary, _ := v.current(agent, time.Now())
	return summary
}
