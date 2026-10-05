package router

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

// nativeTitle names the Activity feed's filter and follow state for the
// shell's pane title.
func (v *liveActivityView) nativeTitle() (string, string) {
	detail := ""
	if v.only {
		rows := v.feedAgents()
		index := slices.IndexFunc(rows, func(row liveActivityRosterRow) bool { return row.agent.Name == v.selected })
		detail = v.painter.Agent(v.selected) + activityui.Dim + fmt.Sprintf(" %d/%d", index+1, len(rows)) + activityui.Undim
	}
	if v.historyHint != "" {
		detail = strings.TrimSpace(activityui.Dim + v.historyHint + activityui.Undim + " " + detail)
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
// into one row. Rows show state, then timer, edited lines, tokens, cost and
// turns; turns drop first when narrow.
func (v *liveActivityView) nativeRoster(width, limit int, now time.Time, focused bool) []string {
	rows := v.roster()
	if len(rows) == 0 || width < 20 {
		return nil
	}
	v.hits = v.hits[:0]
	v.paceRosterMetrics(rows, now)
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
	tokensPartial := false
	for _, row := range rows {
		input += row.agent.InputTokens
		output += row.agent.OutputTokens
		tokensPartial = tokensPartial || row.agent.UsagePartial || row.agent.Turns > 0 && !row.agent.TokensKnown && row.agent.InputTokens == 0 && row.agent.OutputTokens == 0
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
	if v.netCounts != nil {
		totals = append(totals, strings.TrimSpace(diffview.CountStats(*v.netCounts, v.painter.Theme)))
	}
	if input+output > 0 {
		in, out := liveActivityTokenParts(activityPaneAgent{InputTokens: input, OutputTokens: output, UsagePartial: tokensPartial})
		totals = append(totals, "↑"+in+" ↓"+out)
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
	// Lay out agent rows first, so right-aligned metric columns fit their
	// widest visible values rather than fixed widths.
	type agentLine struct {
		row          liveActivityRosterRow
		prefix       string
		finishedLine string
	}
	var laid []agentLine
	var parts [][nativeMetricParts]string
	widest := 0
	for _, item := range items[start:end] {
		if item.finished != nil {
			var names []string
			for _, row := range item.finished {
				names = append(names, activityui.Color(row.agent.Name)+activityui.AgentDisplayName(row.agent.Name)+activityui.Reset)
			}
			line := " " + activityui.Green + "✓" + activityui.Reset + " " + activityui.Dim + fmt.Sprintf("%d finished · ", len(item.finished)) + activityui.Undim + strings.Join(names, activityui.Dim+", "+activityui.Undim)
			laid = append(laid, agentLine{finishedLine: ansi.Truncate(line, width, "…")})
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
		name := activityui.DimColor(row.agent.Name) + tree[:split] + activityui.Undim + color + v.hoverName(tree[split:], row.agent.Name) + activityui.Reset
		gap := "  "
		if focused {
			gap += strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(tree)))
		}
		line := agentLine{row: row, prefix: " " + v.nativeGlyph(row.agent) + " " + name + gap}
		laid = append(laid, line)
		parts = append(parts, nativeRosterMetricParts(v, row.agent, now))
		widest = max(widest, ansi.StringWidth(line.prefix))
	}
	metrics := nativeRosterColumns(parts, width-widest-24)
	skills := v.activeSkills()
	stateEnd := width - 1
	if len(metrics) > 0 {
		stateEnd -= ansi.StringWidth(metrics[0])
	}
	next := 0
	for _, item := range laid {
		if item.finishedLine != "" {
			lines = append(lines, item.finishedLine)
			continue
		}
		room := max(1, stateEnd-ansi.StringWidth(item.prefix))
		// A child's skill count ends its state cell and opens those skills.
		// Main's count is in Main's title.
		label := ""
		if set := skills[item.row.agent.Name]; set != nil && item.row.agent.Name != "/root" {
			label = set.label()
		}
		if room-ansi.StringWidth(label)-2 < 8 {
			label = ""
		}
		state := room
		if label != "" {
			state = room - ansi.StringWidth(label) - 2
		}
		line := item.prefix + liveActivityPad(v.agentState(item.row.agent, state), state)
		if label != "" {
			line += "  " + activityui.Dim + label + activityui.Undim
			v.hits = append(v.hits, liveActivityHit{row: len(lines) + 1, first: stateEnd - ansi.StringWidth(label) + 1, last: stateEnd, agent: item.row.agent.Name, skills: true})
		}
		if next < len(metrics) {
			line += metrics[next]
		}
		next++
		line = ansi.Truncate(line, width, "…")
		if item.row.agent.Name == v.selected && (focused || v.only) {
			line = v.selectRow(line, width)
		}
		lines = append(lines, line)
		v.hits = append(v.hits, liveActivityHit{row: len(lines), first: 1, last: width, agent: item.row.agent.Name})
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

// Metric parts, in column order: context used and percent, elapsed and last
// response, added and removed lines, input and output tokens, cost, and
// roundtrips, and current-round output throughput.
const nativeMetricParts = 11

func nativeRosterMetricParts(v *liveActivityView, agent activityPaneAgent, now time.Time) [nativeMetricParts]string {
	var parts [nativeMetricParts]string
	if agent.Name != "/root" {
		context := contextWindowLabel(agent)
		if used, percent, ok := strings.Cut(context, " • "); ok {
			parts[0], parts[1] = used, percent
		} else if strings.HasSuffix(context, "%") {
			parts[1] = context
		} else {
			parts[0] = context
		}
	}
	_, timer := v.current(agent, now)
	parts[2], parts[3], _ = strings.Cut(timer, " · ")
	if count, ok := v.rosterLines[agent.Name]; ok {
		parts[4], parts[5] = v.lineCountParts(count)
	}
	if agent.TokensKnown || agent.InputTokens > 0 || agent.OutputTokens > 0 {
		input, output := liveActivityTokenParts(agent)
		parts[6], parts[7] = "↑"+input, "↓"+output
	}
	parts[8], parts[9] = liveActivityCost(agent), ansi.Strip(liveActivityTurns(agent))
	parts[10] = outputThroughputLabel(agent.OutputThroughput)
	return parts
}

// rosterMetricEase is how long a changed roster metric takes to reach its new
// value, so usage and edit reports count up rather than jump.
const rosterMetricEase = 500 * time.Millisecond

// rosterMetrics are the roster's paced values. Negative line counts are
// unknown and never interpolate.
type rosterMetrics struct {
	input, output, context, cost, added, removed float64
}

// rosterMetricPace eases one agent's metrics from where they were shown when
// the latest change arrived toward that change.
type rosterMetricPace struct {
	from, to rosterMetrics
	start    time.Time
}

func (p rosterMetricPace) at(now time.Time) rosterMetrics {
	t := min(1, max(0, float64(now.Sub(p.start))/float64(rosterMetricEase)))
	e := 1 - (1-t)*(1-t)*(1-t) // Ease out: fast start, gentle landing.
	lerp := func(from, to float64) float64 {
		if t >= 1 || from < 0 || to < 0 {
			return to
		}
		return from + (to-from)*e
	}
	return rosterMetrics{
		lerp(p.from.input, p.to.input), lerp(p.from.output, p.to.output),
		lerp(p.from.context, p.to.context), lerp(p.from.cost, p.to.cost),
		lerp(p.from.added, p.to.added), lerp(p.from.removed, p.to.removed),
	}
}

// paceRosterMetrics replaces rows' usage with eased values and records the
// line counts to show. A first observation, such as restored history, shows
// its values at once; later changes ease from the value on screen.
func (v *liveActivityView) paceRosterMetrics(rows []liveActivityRosterRow, now time.Time) {
	pace := make(map[string]rosterMetricPace, len(rows))
	v.rosterLines, v.rosterEasing = make(map[string]livediff.Counts), false
	for i, row := range rows {
		agent := &rows[i].agent
		lines, hasLines := v.lineCounts[agent.Name]
		target := rosterMetrics{float64(agent.InputTokens), float64(agent.OutputTokens), float64(agent.ContextTokens), agent.Cost, float64(lines.Added), float64(lines.Removed)}
		p, seen := v.rosterPace[row.agent.Name]
		switch {
		case !seen:
			p = rosterMetricPace{from: target, to: target} // Settled.
		case p.to != target:
			p = rosterMetricPace{p.at(now), target, now}
		}
		pace[agent.Name] = p
		v.rosterEasing = v.rosterEasing || now.Sub(p.start) < rosterMetricEase
		shown := p.at(now)
		agent.InputTokens, agent.OutputTokens = uint64(math.Round(shown.input)), uint64(math.Round(shown.output))
		agent.ContextTokens, agent.Cost = uint64(math.Round(shown.context)), shown.cost
		if hasLines {
			v.rosterLines[agent.Name] = livediff.Counts{Added: int(math.Round(shown.added)), Removed: int(math.Round(shown.removed))}
		}
	}
	v.rosterPace = pace
}

// lineCountParts uses semantic green/red colors, independent of the syntax
// theme. Zero and unknown counts are omitted.
func (v *liveActivityView) lineCountParts(count livediff.Counts) (string, string) {
	if count.Added < 0 || count.Removed < 0 {
		return "", ""
	}
	var added, removed string
	if count.Added > 0 {
		added = activityui.Green + fmt.Sprintf("+%d", count.Added) + "\x1b[39m"
	}
	if count.Removed > 0 {
		removed = activityui.Red + fmt.Sprintf("-%d", count.Removed) + "\x1b[39m"
	}
	return added, removed
}

// nativeRosterColumns renders each row's metrics as columns sized to their
// widest value. Context is retained longest as lower-priority columns drop to
// fit room. Within a column, the part before its separator is right-aligned
// and the part after it left-aligned, so separators line up across rows
// while each value stays next to its separator. Metrics are uniformly
// secondary text.
func nativeRosterColumns(rows [][nativeMetricParts]string, room int) []string {
	var widths [nativeMetricParts]int
	for _, row := range rows {
		for i, part := range row {
			widths[i] = max(widths[i], ansi.StringWidth(part))
		}
	}
	type column struct {
		first, second int // Part indexes; second < 0 for a single part.
		separator     string
	}
	var columns []column
	for _, c := range []column{{0, 1, " • "}, {10, -1, ""}, {2, 3, " · "}, {4, 5, " "}, {6, 7, " "}, {8, -1, ""}, {9, -1, ""}} {
		if widths[c.first] > 0 || c.second >= 0 && widths[c.second] > 0 {
			columns = append(columns, c)
		}
	}
	columnWidth := func(c column) int {
		if c.second < 0 {
			return widths[c.first]
		}
		if widths[c.first] == 0 || widths[c.second] == 0 {
			return widths[c.first] + widths[c.second]
		}
		return widths[c.first] + ansi.StringWidth(c.separator) + widths[c.second]
	}
	for len(columns) > 0 {
		total := 0
		for _, c := range columns {
			total += 2 + columnWidth(c)
		}
		if total <= room {
			break
		}
		columns = columns[:len(columns)-1]
	}
	if len(columns) == 0 {
		return nil
	}
	lines := make([]string, len(rows))
	for r, row := range rows {
		var b strings.Builder
		for _, c := range columns {
			b.WriteString("  ")
			switch {
			case c.second < 0:
				b.WriteString(rosterAlign(row[c.first], widths[c.first]))
			case widths[c.first] == 0 || widths[c.second] == 0:
				b.WriteString(rosterAlign(row[c.first]+row[c.second], columnWidth(c)))
			case row[c.first] == "" && row[c.second] == "":
				b.WriteString(strings.Repeat(" ", columnWidth(c)))
			case row[c.first] == "":
				// A lone second part, such as unknown context's 0%, keeps its column.
				b.WriteString(strings.Repeat(" ", widths[c.first]+ansi.StringWidth(c.separator)) + liveActivityPad(row[c.second], widths[c.second]))
			default:
				b.WriteString(rosterAlign(row[c.first], widths[c.first]) + c.separator + liveActivityPad(row[c.second], widths[c.second]))
			}
		}
		lines[r] = activityui.Dim + b.String() + activityui.Undim
	}
	return lines
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
func (v *liveActivityView) agentState(agent activityPaneAgent, width int) string {
	source, owner := v, agent.Name
	if agent.Name == "/root" && v.mainView != nil {
		source, owner = v.mainView, "Main"
	}
	var blocks []activityui.Block
	var kind string
	for i, entry := range slices.Backward(source.entries) {
		if entry.Agent == owner || source == v && entry.Agent == agent.Name {
			blocks, kind = source.entries[i].blocks, entry.Kind
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
	summary, _ := v.current(agent, time.Now(), width)
	return summary
}
