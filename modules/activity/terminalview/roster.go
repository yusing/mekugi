package terminalview

// Source: apps/mekugi/internal/router/native_roster.go:18:474@[29de53cc].

import (
	"cmp"
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

// RosterSummary carries the subagent facts the session stat lines show in
// place of Main's roster row: glyph counts, net edited lines and cost.
type RosterSummary struct {
	Counts   []string // Painted working, finished and error counts; zero counts are omitted.
	Net      string
	Cost     string // Session total, including Main.
	Children bool
}

func (v *View) RosterSummary() RosterSummary {
	var s RosterSummary
	var working, finished, errors int
	var cost float64
	costKnown, partial := false, false
	for _, row := range v.Roster() {
		if row.Agent.CostKnown {
			cost, costKnown = cost+row.Agent.Cost, true
			partial = partial || row.Agent.CostPartial
		} else if row.Agent.Turns > 0 {
			partial = true
		}
		if row.Agent.Name == "/root" {
			continue
		}
		s.Children = true
		switch v.agentStatus(row.Agent) {
		case '◐':
			working++
		case '✓':
			finished++
		case '!':
			errors++
		}
	}
	if working > 0 {
		s.Counts = append(s.Counts, v.Painter.Theme.Warning()+fmt.Sprintf("◐ %d", working)+terminalui.Reset)
	}
	if finished > 0 {
		s.Counts = append(s.Counts, terminalui.Dim+fmt.Sprintf("✓ %d", finished)+terminalui.Undim)
	}
	if errors > 0 {
		s.Counts = append(s.Counts, v.Painter.Theme.Danger()+fmt.Sprintf("! %d", errors)+terminalui.Reset)
	}
	if v.NetCounts != nil {
		s.Net = strings.TrimSpace(diffrender.CountStats(*v.NetCounts, v.Painter.Theme))
	}
	if costKnown {
		prefix := "$"
		if partial {
			prefix = "≥$"
		}
		s.Cost = prefix + fmt.Sprintf("%.2f", cost)
	}
	return s
}

// AgentUsage formats caller's input/output tokens and estimated cost; unknown
// values are empty.
// AgentUsage is caller's painted input and output tokens and its cost.
func (v *View) AgentUsage(caller string) (tokens, cost string) {
	for _, row := range v.roster {
		if row.Agent.Name != caller {
			continue
		}
		if agent := row.Agent; agent.InputTokens+agent.OutputTokens > 0 {
			in, out := tokenParts(agent)
			tokens = "↑ " + in + " ↓ " + out
		}
		return tokens, Cost(row.Agent)
	}
	return "", ""
}

// ParentCaller names the nearest roster ancestor of caller, or Main.
func (v *View) ParentCaller(caller string) string {
	parent := "/root"
	for _, row := range v.roster {
		name := row.Agent.Name
		if len(name) > len(parent) && strings.HasPrefix(caller, name+"/") {
			parent = name
		}
	}
	return parent
}

// ShowParent displays the displayed child's roster parent.
func (v *View) ShowParent() {
	if v.displayingChild() {
		v.requestDisplay(v.ParentCaller(v.displayedCaller()))
		v.Follow()
	}
}

// RosterSheet paints subagent rows and their hit targets below a heading row
// the caller paints at index 0; hits count that row. Main is omitted because
// the session stat lines carry its facts. At most limit rows follow the
// heading, scrolled to keep the displayed agent.
func (v *View) RosterSheet(width, limit int, now time.Time) []string {
	rows := slices.DeleteFunc(v.Roster(), func(row activitypresentation.AgentRow) bool { return row.Agent.Name == "/root" })
	v.Hits = v.Hits[:0]
	if len(rows) == 0 || width < 20 || limit < 1 {
		return nil
	}
	selectedCaller := cmp.Or(v.displayedCaller(), "/root")
	v.paceRosterMetrics(rows, now)
	lines := []string{""}
	selected := max(0, slices.IndexFunc(rows, func(row activitypresentation.AgentRow) bool { return row.Agent.Name == selectedCaller }))
	count := min(len(rows), limit)
	if len(rows) > limit && limit > 1 {
		count-- // Room for every hidden row, including those above selection.
	}
	start := max(0, min(selected-count+1, len(rows)-count))
	if v.RosterSelected != selectedCaller {
		v.RosterManual = false
	}
	if v.RosterManual {
		start = min(v.RosterOffset, len(rows)-count)
	}
	end := start + count
	visible := rows[start:end]
	hidden := len(rows) - len(visible)
	nameWidth := 4
	for index := range visible {
		name, _ := RosterTree(visible, index, 0)
		nameWidth = max(nameWidth, ansi.StringWidth(name))
	}
	nameWidth = min(nameWidth, max(8, width/4))
	// Lay out agent rows first, so right-aligned metric columns fit their
	// widest visible values rather than fixed widths.
	type agentLine struct {
		row    activitypresentation.AgentRow
		prefix string
	}
	var laid []agentLine
	var parts [][MetricParts]string
	widest := 0
	for index, row := range visible {
		tree, _ := RosterTree(visible, index, 0)
		tree = strings.TrimRight(Middle(tree, nameWidth), " ")
		split := strings.LastIndexAny(tree, " /") + 1
		color := terminalui.AgentColor(v.Painter.Theme, row.Agent.Name)
		if color == "" {
			color = "\x1b[1m" + v.Painter.Theme.Accent()
		}
		name := terminalui.DimAgentColor(v.Painter.Theme, row.Agent.Name) + tree[:split] + terminalui.Undim + color + v.hoverName(tree[split:], row.Agent.Name) + terminalui.Reset
		line := agentLine{row: row, prefix: " " + v.NativeGlyph(row.Agent) + " " + name + "  "}
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
		room := max(1, stateEnd-ansi.StringWidth(item.prefix))
		// A skill count ends its agent's state cell and opens those skills.
		label := ""
		if set := item.row.Agent.Skills; set != nil {
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
		if item.row.Agent.Name == selectedCaller {
			line = v.selectRow(line, width)
		}
		lines = append(lines, line)
		v.Hits = append(v.Hits, Hit{Row: len(lines), First: 1, Last: width, Agent: item.row.Agent.Name})
	}
	if hidden > 0 && limit > 1 {
		lines = append(lines, ansi.Truncate(terminalui.Dim+fmt.Sprintf("   +%d more", hidden)+terminalui.Undim, width, "…"))
	}
	v.RosterOffset, v.rosterEnd, v.RosterSelected = start, end, selectedCaller
	return lines
}

// RoleLegend pairs each observed subagent role with its glyph color, within
// width. When the full legend cannot fit, the displayed agent's role leads.
func (v *View) RoleLegend(width int) string {
	var roles []string
	selected := ""
	for _, row := range v.Roster() {
		role := Role(row.Agent)
		if row.Agent.Name == "/root" || role == "" {
			continue
		}
		if row.Agent.Name == v.displayedCaller() {
			selected = role
		}
		if !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
	}
	var labels []string
	for _, role := range roles {
		labels = append(labels, v.RoleColor(role)+"●"+terminalui.Reset+" "+role)
	}
	if ansi.StringWidth(strings.Join(labels, "  ")) > width {
		if index := slices.Index(roles, selected); index > 0 {
			labels[0], labels[index] = labels[index], labels[0]
		}
	}
	return ansi.Truncate(strings.Join(labels, "  "), width, "…")
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
	timer := agentTimer(agent, now)
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
		added = v.Painter.Theme.Success() + fmt.Sprintf("+%d", count.Added) + "\x1b[39m"
	}
	if count.Removed > 0 {
		removed = v.Painter.Theme.Danger() + fmt.Sprintf("-%d", count.Removed) + "\x1b[39m"
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
	color := terminalui.AgentColor(v.Painter.Theme, agent.Name)
	if color == "" {
		color = v.Painter.Theme.Accent()
	}
	switch v.agentStatus(agent) {
	case '◐':
		return color + "◐" + terminalui.Reset
	case '!':
		return v.Painter.Theme.Danger() + "!" + terminalui.Reset
	case '✓':
		return v.Painter.Theme.Success() + "✓" + terminalui.Reset
	}
	return color + "●" + terminalui.Reset
}

// AgentState preserves lifecycle states when idle, and shares the detailed
// activity summary while an agent is working.
func (v *View) AgentState(agent activitypresentation.Agent, width int, now time.Time) string {
	switch agent.Status {
	case "paused":
		return terminalui.Dim + "paused" + terminalui.Undim
	case "waiting":
		wait := Block{Kind: "progress", Body: "Waiting for", Live: true}
		if agent.Wait != nil {
			wait.Value = *agent.Wait
		}
		if len(wait.WaitTargets) == 0 && agent.WaitingFor != "" {
			wait.Body += " " + agent.WaitingFor
		}
		return v.Painter.Summary([]Block{wait}, width)
	case "error":
		return v.Painter.Theme.Danger() + "error" + terminalui.Reset
	case "answered":
		return terminalui.Dim + "done" + terminalui.Undim
	case "":
		return terminalui.Dim + "idle" + terminalui.Undim
	case "responding":
		if len(agent.Summary) == 0 && agent.Generating == "" {
			return "working"
		}
	default:
		return terminalui.Dim + agent.Status + terminalui.Undim
	}
	summary, _ := v.Current(agent, now, width)
	return summary
}
