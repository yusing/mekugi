package terminalview

// Source: apps/mekugi/internal/router/native_roster.go:18:474@[29de53cc].

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/diffrender"
)

// nativeRosterItem is one roster row: an agent, or the fold of finished ones.
type nativeRosterItem struct {
	row      activitypresentation.AgentRow
	finished []activitypresentation.AgentRow
}

// NativeRoster fits the roster to its content within limit rows, under a rule
// carrying the counts and session totals. Unfocused, finished agents fold
// into one row. Rows show state, then timer, edited lines, tokens, cost and
// turns; turns drop first when narrow.
func (v *View) NativeRoster(width, limit int, now time.Time, focused bool) []string {
	rows := v.Roster()
	if len(rows) == 0 || width < 20 {
		return nil
	}
	v.Hits = v.Hits[:0]
	v.paceRosterMetrics(rows, now)
	var items []nativeRosterItem
	fold := -1
	for _, row := range rows {
		if !focused && row.Agent.Name != "/root" && v.agentStatus(row.Agent) == '✓' && !(v.displayingChild() && row.Agent.Name == v.displayedCaller()) {
			if fold < 0 {
				fold = len(items)
				items = append(items, nativeRosterItem{})
			}
			items[fold].finished = append(items[fold].finished, row)
			continue
		}
		items = append(items, nativeRosterItem{row: row})
	}
	responding, errors := v.StatusCounts(rows)
	var input, output uint64
	var cost float64
	costKnown, partial := false, false
	tokensPartial := false
	for _, row := range rows {
		input += row.Agent.InputTokens
		output += row.Agent.OutputTokens
		tokensPartial = tokensPartial || row.Agent.UsagePartial || row.Agent.Turns > 0 && !row.Agent.TokensKnown && row.Agent.InputTokens == 0 && row.Agent.OutputTokens == 0
		if row.Agent.CostKnown {
			cost, costKnown = cost+row.Agent.Cost, true
			partial = partial || row.Agent.CostPartial
		} else if row.Agent.Turns > 0 {
			partial = true
		}
	}
	detail := terminalui.Dim + fmt.Sprintf("%d", len(rows)) + terminalui.Undim
	if responding > 0 {
		detail += terminalui.Dim + " · " + terminalui.Undim + terminalui.Amber + fmt.Sprintf("%d working", responding) + terminalui.Reset
	}
	if errors > 0 {
		detail += terminalui.Dim + " · " + terminalui.Undim + terminalui.Red + fmt.Sprintf("%d error", errors) + terminalui.Reset
	}
	var totals []string
	if v.NetCounts != nil {
		totals = append(totals, strings.TrimSpace(diffrender.CountStats(*v.NetCounts, v.Painter.Theme)))
	}
	if input+output > 0 {
		in, out := tokenParts(activitypresentation.Agent{AgentFacts: activitypresentation.AgentFacts{InputTokens: input, OutputTokens: output, UsagePartial: tokensPartial}})
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
		right = terminalui.Dim + strings.Join(totals, " · ") + terminalui.Undim
	}
	lines := []string{terminalui.PaneRule("─", "─", "─", terminalui.PaneTitle(3, "Agents", detail, focused), right, width, terminalui.PaneBorder(focused))}

	limit = max(1, limit)
	legend := ""
	if focused && limit > 1 {
		var roles []string
		for _, row := range rows {
			if role := Role(row.Agent); role != "" && !slices.Contains(roles, role) {
				roles = append(roles, role)
			}
		}
		if len(roles) > 0 {
			var labels []string
			for _, role := range roles {
				labels = append(labels, v.RoleColor(role)+"●"+terminalui.Reset+" "+role)
			}
			// Keep the selected role discoverable when the full legend cannot fit.
			if ansi.StringWidth(" Roles: "+strings.Join(labels, "  ")) > width {
				for _, row := range rows {
					if row.Agent.Name == v.Selected {
						if index := slices.Index(roles, Role(row.Agent)); index > 0 {
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
	selected := max(0, slices.IndexFunc(items, func(item nativeRosterItem) bool { return item.finished == nil && item.row.Agent.Name == v.Selected }))
	start := 0
	if len(items) > limit {
		start = max(0, min(selected-limit+2, len(items)-limit+1))
	}
	end := min(len(items), start+limit)
	if end < len(items) {
		end-- // Room for the hidden-count row.
	}
	nameWidth := 4
	for _, item := range items[start:end] {
		if item.finished != nil {
			continue
		}
		name, _ := RosterTree(rows, slices.IndexFunc(rows, func(other activitypresentation.AgentRow) bool { return other.Agent.Name == item.row.Agent.Name }), 0)
		nameWidth = max(nameWidth, ansi.StringWidth(name))
	}
	nameWidth = min(nameWidth, max(8, width/4))
	// Lay out agent rows first, so right-aligned metric columns fit their
	// widest visible values rather than fixed widths.
	type agentLine struct {
		row          activitypresentation.AgentRow
		prefix       string
		finishedLine string
	}
	var laid []agentLine
	var parts [][MetricParts]string
	widest := 0
	for _, item := range items[start:end] {
		if item.finished != nil {
			var names []string
			for _, row := range item.finished {
				names = append(names, terminalui.AgentColor(row.Agent.Name)+terminalui.AgentDisplayName(row.Agent.Name)+terminalui.Reset)
			}
			line := " " + terminalui.Green + "✓" + terminalui.Reset + " " + terminalui.Dim + fmt.Sprintf("%d finished · ", len(item.finished)) + terminalui.Undim + strings.Join(names, terminalui.Dim+", "+terminalui.Undim)
			laid = append(laid, agentLine{finishedLine: ansi.Truncate(line, width, "…")})
			continue
		}
		row := item.row
		index := slices.IndexFunc(rows, func(other activitypresentation.AgentRow) bool { return other.Agent.Name == row.Agent.Name })
		tree, _ := RosterTree(rows, index, 0)
		tree = strings.TrimRight(Middle(tree, nameWidth), " ")
		split := strings.LastIndexAny(tree, " /") + 1
		color := terminalui.AgentColor(row.Agent.Name)
		if color == "" {
			color = "\x1b[1m" + v.Painter.Theme.Accent()
		}
		name := terminalui.DimAgentColor(row.Agent.Name) + tree[:split] + terminalui.Undim + color + v.hoverName(tree[split:], row.Agent.Name) + terminalui.Reset
		gap := "  "
		if focused {
			gap += strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(tree)))
		}
		line := agentLine{row: row, prefix: " " + v.NativeGlyph(row.Agent) + " " + name + gap}
		laid = append(laid, line)
		parts = append(parts, RosterMetricParts(v, row.Agent, now))
		widest = max(widest, ansi.StringWidth(line.prefix))
	}
	metrics := RosterColumns(parts, width-widest-24)
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
		if set := item.row.Agent.Skills; set != nil && item.row.Agent.Name != "/root" {
			label = SkillLabel(set)
		}
		if room-ansi.StringWidth(label)-2 < 8 {
			label = ""
		}
		state := room
		if label != "" {
			state = room - ansi.StringWidth(label) - 2
		}
		line := item.prefix + liveActivityPad(v.AgentState(item.row.Agent, state, now), state)
		if label != "" {
			styled := label
			if v.SkillsHover == item.row.Agent.Name {
				styled = Underline(styled)
			}
			line += "  " + terminalui.Dim + styled + terminalui.Undim
			v.Hits = append(v.Hits, Hit{Row: len(lines) + 1, First: stateEnd - ansi.StringWidth(label) + 1, Last: stateEnd, Agent: item.row.Agent.Name, Skills: true})
		}
		if next < len(metrics) {
			line += metrics[next]
		}
		next++
		line = ansi.Truncate(line, width, "…")
		if focused && item.row.Agent.Name == v.Selected || v.displayingChild() && item.row.Agent.Name == v.displayedCaller() {
			line = v.selectRow(line, width)
		}
		lines = append(lines, line)
		v.Hits = append(v.Hits, Hit{Row: len(lines), First: 1, Last: width, Agent: item.row.Agent.Name})
	}
	if hidden := len(items) - end; hidden > 0 {
		lines = append(lines, ansi.Truncate(terminalui.Dim+fmt.Sprintf("   +%d more · ^B 3 shows all", hidden)+terminalui.Undim, width, "…"))
	}
	if legend != "" {
		lines = append(lines, legend)
	}
	v.RosterOffset, v.rosterEnd = start, end
	return lines
}

// Metric parts, in column order: context used and percent, elapsed and last
// response, added and removed lines, input and output tokens, cost, and
// roundtrips, and current-round output throughput.
const MetricParts = 11

func RosterMetricParts(v *View, agent activitypresentation.Agent, now time.Time) [MetricParts]string {
	var parts [MetricParts]string
	if agent.Name != "/root" {
		// Main's context and output rate belong to the composer.
		for _, item := range agent.StatusItems {
			if parts[10] != "" {
				parts[10] += " • "
			}
			parts[10] += item.Text
		}
		context := ContextWindowLabel(agent)
		if used, percent, ok := strings.Cut(context, " • "); ok {
			parts[0], parts[1] = used, percent
		} else if strings.HasSuffix(context, "%") {
			parts[1] = context
		} else {
			parts[0] = context
		}
	}
	_, timer := v.Current(agent, now)
	parts[2], parts[3], _ = strings.Cut(timer, " · ")
	if count, ok := v.RosterLines[agent.Name]; ok {
		parts[4], parts[5] = v.LineCountParts(count)
	}
	if agent.TokensKnown || agent.InputTokens > 0 || agent.OutputTokens > 0 {
		input, output := tokenParts(agent)
		parts[6], parts[7] = "↑"+input, "↓"+output
	}
	parts[8], parts[9] = Cost(agent), ansi.Strip(Turns(agent))
	return parts
}

// MetricEase is how long a changed roster metric takes to reach its new
// value, so usage and edit reports count up rather than jump.
const MetricEase = 500 * time.Millisecond

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
	t := min(1, max(0, float64(now.Sub(p.start))/float64(MetricEase)))
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
func (v *View) paceRosterMetrics(rows []activitypresentation.AgentRow, now time.Time) {
	pace := make(map[string]rosterMetricPace, len(rows))
	v.RosterLines, v.RosterEasing = make(map[string]diffmodel.Counts), false
	for i, row := range rows {
		agent := &rows[i].Agent
		lines, hasLines := v.LineCounts[agent.Name]
		target := rosterMetrics{float64(agent.InputTokens), float64(agent.OutputTokens), float64(agent.ContextTokens), agent.Cost, float64(lines.Added), float64(lines.Removed)}
		p, seen := v.rosterPace[row.Agent.Name]
		switch {
		case !seen:
			p = rosterMetricPace{from: target, to: target} // Settled.
		case p.to != target:
			p = rosterMetricPace{p.at(now), target, now}
		}
		pace[agent.Name] = p
		v.RosterEasing = v.RosterEasing || now.Sub(p.start) < MetricEase
		shown := p.at(now)
		agent.InputTokens, agent.OutputTokens = uint64(math.Round(shown.input)), uint64(math.Round(shown.output))
		agent.ContextTokens, agent.Cost = uint64(math.Round(shown.context)), shown.cost
		if hasLines {
			v.RosterLines[agent.Name] = diffmodel.Counts{Added: int(math.Round(shown.added)), Removed: int(math.Round(shown.removed))}
		}
	}
	v.rosterPace = pace
}

// LineCountParts uses semantic green/red colors, independent of the syntax
// theme. Zero and unknown counts are omitted.
func (v *View) LineCountParts(count diffmodel.Counts) (string, string) {
	if count.Added < 0 || count.Removed < 0 {
		return "", ""
	}
	var added, removed string
	if count.Added > 0 {
		added = terminalui.Green + fmt.Sprintf("+%d", count.Added) + "\x1b[39m"
	}
	if count.Removed > 0 {
		removed = terminalui.Red + fmt.Sprintf("-%d", count.Removed) + "\x1b[39m"
	}
	return added, removed
}

// RosterColumns renders each row's metrics as columns sized to their
// widest value. Context is retained longest as lower-priority columns drop to
// fit room. Within a column, the part before its separator is right-aligned
// and the part after it left-aligned, so separators line up across rows
// while each value stays next to its separator. Metrics are uniformly
// secondary text.
func RosterColumns(rows [][MetricParts]string, room int) []string {
	var widths [MetricParts]int
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
		lines[r] = terminalui.Dim + b.String() + terminalui.Undim
	}
	return lines
}

// rosterAlign right-aligns text within width columns.
func rosterAlign(text string, width int) string {
	return strings.Repeat(" ", max(0, width-ansi.StringWidth(text))) + text
}

func ContextWindowLabel(agent activitypresentation.Agent) string {
	if !agent.ContextKnown {
		return "0%"
	}
	used := FormatUsageTokens(agent.ContextTokens)
	if agent.ContextWindow == 0 {
		return used + " used"
	}
	return fmt.Sprintf("%s/%s • %.0f%%", used, FormatUsageTokens(agent.ContextWindow), 100*float64(agent.ContextTokens)/float64(agent.ContextWindow))
}

// NativeGlyph uses role colors when known; the glyph shape preserves status.
// Names retain the agent identity colors shared with Main and Activity.
func (v *View) NativeGlyph(agent activitypresentation.Agent) string {
	if role := Role(agent); role != "" {
		return v.Glyph(agent)
	}
	color := terminalui.AgentColor(agent.Name)
	if color == "" {
		color = v.Painter.Theme.Accent()
	}
	switch v.agentStatus(agent) {
	case '◐':
		return color + "◐" + terminalui.Reset
	case '!':
		return terminalui.Red + "!" + terminalui.Reset
	case '✓':
		return terminalui.Green + "✓" + terminalui.Reset
	}
	return color + "●" + terminalui.Reset
}

// AgentState preserves lifecycle states when idle, and shares the detailed
// activity summary while an agent is working.
func (v *View) AgentState(agent activitypresentation.Agent, width int, now time.Time) string {
	switch {
	case agent.LatestKind == "error":
		return terminalui.Red + "error" + terminalui.Reset
	case !agent.Responding && agent.Final:
		return terminalui.Dim + "done" + terminalui.Undim
	case !agent.Responding:
		return terminalui.Dim + "idle" + terminalui.Undim
	case len(agent.Summary) == 0:
		return "working"
	}
	summary, _ := v.Current(agent, now, width)
	return summary
}
