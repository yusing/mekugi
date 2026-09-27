package router

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// nativeTitle names the Activity feed's filter and follow state for the
// shell's pane title.
func (v *liveActivityView) nativeTitle() (string, string) {
	detail := ""
	if v.only {
		rows := v.roster()
		index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
		detail = v.painter.Agent(v.selected) + activityui.Dim + fmt.Sprintf(" %d/%d", index+1, len(rows)) + activityui.Undim
	}
	state := v.status
	if state == "" {
		state = scrollLabel(v)
		if v.following {
			state = strings.TrimSpace(state + " " + activityui.Dim + "FOLLOW" + activityui.Undim)
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
// into one row. Rows show state, then timer, tokens, cost and turns;
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
	detail := activityui.Dim + fmt.Sprintf("%d", len(rows)) + activityui.Undim
	if responding > 0 {
		detail += activityui.Dim + " · " + activityui.Undim + activityui.Amber + fmt.Sprintf("%d working", responding) + activityui.Reset
	}
	if errors > 0 {
		detail += activityui.Dim + " · " + activityui.Undim + activityui.Red + fmt.Sprintf("%d error", errors) + activityui.Reset
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
		right = activityui.Dim + strings.Join(totals, " · ") + activityui.Undim
	}
	lines := []string{nativeRule("─", "─", "─", nativeTitle(4, "Agents", detail, focused), right, width, nativeBorder(focused))}

	limit = max(1, limit)
	legend := ""
	if focused && limit > 1 {
		var roles []string
		for _, row := range rows {
			if role := liveActivityRole(row.agent); role != "" && !slices.Contains(roles, role) {
				roles = append(roles, role)
			}
		}
		if len(roles) > 0 {
			var labels []string
			for _, role := range roles {
				labels = append(labels, v.roleColor(role)+"●"+activityui.Reset+" "+role)
			}
			// Keep the selected role discoverable when the full legend cannot fit.
			if ansi.StringWidth(" Roles: "+strings.Join(labels, "  ")) > width {
				for _, row := range rows {
					if row.agent.Name == v.selected {
						if index := slices.Index(roles, liveActivityRole(row.agent)); index > 0 {
							labels[0], labels[index] = labels[index], labels[0]
						}
						break
					}
				}
			}
			legend = ansi.Truncate(" Roles: "+strings.Join(labels, "  "), width, "…")
			limit--
		}
	}
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
				names = append(names, activityui.Color(row.agent.Name)+activityui.AgentDisplayName(row.agent.Name)+activityui.Reset)
			}
			line := " " + activityui.Green + "✓" + activityui.Reset + " " + activityui.Dim + fmt.Sprintf("%d finished · ", len(item.finished)) + activityui.Undim + strings.Join(names, activityui.Dim+", "+activityui.Undim)
			lines = append(lines, ansi.Truncate(line, width, "…"))
			continue
		}
		row := item.row
		index := slices.IndexFunc(rows, func(other liveActivityRosterRow) bool { return other.agent.Name == row.agent.Name })
		tree, _ := rosterTree(rows, index, 0)
		tree = strings.TrimRight(liveActivityMiddle(tree, nameWidth), " ")
		split := strings.LastIndexAny(tree, " /") + 1
		color := activityui.Color(row.agent.Name)
		if color == "" {
			color = "\x1b[1m" + v.painter.Theme.Accent()
		}
		name := activityui.Dim + tree[:split] + activityui.Undim + color + v.hoverName(tree[split:], row.agent.Name) + activityui.Reset
		gap := "  "
		if focused {
			gap += strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(tree)))
		}
		line := " " + v.nativeGlyph(row.agent) + " " + name + gap
		state := v.agentState(row.agent)
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
		lines = append(lines, ansi.Truncate(activityui.Dim+fmt.Sprintf("   +%d more · ^B 4 shows all", hidden)+activityui.Undim, width, "…"))
	}
	if legend != "" {
		lines = append(lines, legend)
	}
	v.rosterOffset, v.rosterEnd = start, end
	return lines
}

// nativeRosterMetrics retains context longest as lower-priority metrics drop.
// Metrics are uniformly secondary text. Within a metric, the part before its
// separator is right-aligned and the part after it left-aligned, so separators
// line up across rows while each value stays next to its separator.
func nativeRosterMetrics(v *liveActivityView, agent activityPaneAgent, now time.Time, room int) string {
	context := contextWindowLabel(agent)
	if used, percent, ok := strings.Cut(context, " • "); ok {
		context = rosterAlign(used, 11) + " • " + liveActivityPad(percent, 3)
	}
	_, timer := v.current(agent, now)
	if elapsed, last, ok := strings.Cut(timer, " · "); ok {
		timer = rosterAlign(elapsed, 6) + " · " + liveActivityPad(last, 10)
	}
	tokens := ""
	if agent.InputTokens+agent.OutputTokens > 0 {
		tokens = rosterAlign("↑"+formatUsageTokens(agent.InputTokens), 7) + " " + liveActivityPad("↓"+formatUsageTokens(agent.OutputTokens), 7)
	}
	cells := []struct {
		text  string
		width int
	}{{context, 17}, {timer, 19}, {tokens, 15}, {liveActivityCost(agent), 7}, {ansi.Strip(liveActivityTurns(agent)), 5}}
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
	if b.Len() == 0 {
		return ""
	}
	return activityui.Dim + b.String() + activityui.Undim
}

// rosterAlign right-aligns text within width columns.
func rosterAlign(text string, width int) string {
	return strings.Repeat(" ", max(0, width-ansi.StringWidth(text))) + text
}

func contextWindowLabel(agent activityPaneAgent) string {
	if !agent.ContextKnown {
		return "0%"
	}
	used := formatUsageTokens(agent.ContextTokens)
	if agent.ContextWindow == 0 {
		return used + " used"
	}
	return fmt.Sprintf("%s/%s • %.0f%%", used, formatUsageTokens(agent.ContextWindow), 100*float64(agent.ContextTokens)/float64(agent.ContextWindow))
}

// nativeGlyph uses role colors when known; the glyph shape preserves status.
// Names retain the agent identity colors shared with Main and Activity.
func (v *liveActivityView) nativeGlyph(agent activityPaneAgent) string {
	if role := liveActivityRole(agent); role != "" {
		return v.glyph(agent)
	}
	color := activityui.Color(agent.Name)
	if color == "" {
		color = v.painter.Theme.Accent()
	}
	switch v.agentStatus(agent) {
	case '◐':
		return color + "◐" + activityui.Reset
	case '!':
		return activityui.Red + "!" + activityui.Reset
	case '✓':
		return activityui.Green + "✓" + activityui.Reset
	}
	return color + "●" + activityui.Reset
}

// agentState preserves lifecycle states when idle, and shares the detailed
// activity summary while an agent is working.
func (v *liveActivityView) agentState(agent activityPaneAgent) string {
	source, owner := v, agent.Name
	if agent.Name == "/root" && v.mainView != nil {
		source, owner = v.mainView, "Main"
	}
	var blocks []activityui.Block
	var kind string
	for i, entry := range slices.Backward(source.entries) {
		if entry.Agent == owner || source == v && entry.Agent == agent.Name {
			blocks, kind = source.blocks[i], entry.Kind
			break
		}
	}
	switch {
	case kind == "error":
		return activityui.Red + "error" + activityui.Reset
	case !agent.Responding && agent.Final:
		return activityui.Dim + "done" + activityui.Undim
	case !agent.Responding:
		return activityui.Dim + "idle" + activityui.Undim
	case len(blocks) == 0:
		return "working"
	}
	summary, _ := v.current(agent, time.Now())
	return summary
}
