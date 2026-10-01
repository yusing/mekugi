package router

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

type nativeJournalCard struct {
	Journal threadJournal
	Since   uint64
}

type nativeJournalView struct {
	unscoped bool
	// expanded records explicit disclosure choices; absent nodes fit automatically.
	expanded                 map[string]bool
	selected, offset, height int
	top                      int // Header rows above the first node row.
	// hover is the pointed pane row plus one. The selection is filled only
	// while the keyboard drives it, so no row stays marked without a pointer.
	hover          int
	cursor, reveal bool
	rows           []journalPaneRow
}

type journalPaneRow struct {
	node  journalNode
	lead  string // Tree guides before the disclosure column.
	depth int
	last  bool // The last of its siblings.
	open  bool // Its children are shown.
}

// journalRowMargin is the blank column between the pane frame and the tree.
const journalRowMargin = 1

func (r journalPaneRow) expandable() bool { return len(r.node.Children) > 0 }

// disclosure is the pane column span a click toggles.
func (r journalPaneRow) disclosure() (int, int) {
	return journalRowMargin, journalRowMargin + ansi.StringWidth(r.lead) + 2
}

func (s *nativeJournalSink) presented() *threadJournal {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mounted != nil {
		return s.mounted
	}
	return s.tree
}

func (u *appServerUI) journalTreeSnapshot() *threadJournal {
	return u.selectedJournalSink().presented()
}

func (u *appServerUI) selectedJournalSink() *nativeJournalSink {
	sinks := []*nativeJournalSink{u.journal, u.unscopedJournal}
	if u.journalView.unscoped {
		slices.Reverse(sinks)
	}
	for _, sink := range sinks {
		if sink.presented() != nil {
			return sink
		}
	}
	return nil
}

// journalNamespaces names the presented namespace and the one n switches to.
// Requests without workspace metadata keep a separate unscoped journal; most
// sessions have only one, and then the pane names neither.
func (u *appServerUI) journalNamespaces() (current, other string) {
	if u.journal.presented() == nil || u.unscopedJournal.presented() == nil {
		return "", ""
	}
	if u.selectedJournalSink() == u.unscopedJournal {
		return "unscoped", "workspace"
	}
	return "workspace", "unscoped"
}

// The last layout alone is stale when a key closed the pane since that paint;
// a suppressed note would then show nowhere and still be acknowledged.
func (u *appServerUI) journalPanePresents(sink *nativeJournalSink) bool {
	s := u.shell
	return s != nil && s.journalOpen && s.focus != 1 && s.focus != 2 && !s.diffOpen && s.layout.journal.w > 0 && s.output == nil && u.selectedJournalSink() == sink
}

func journalNodeClosed(node journalNode) bool {
	return node.State == "done" || node.State == "dropped"
}

// rebuild keeps the selection on its node as rows arrive, move or collapse.
func (v *nativeJournalView) rebuild(j *threadJournal) {
	selected := ""
	if v.selected >= 0 && v.selected < len(v.rows) {
		selected = v.rows[v.selected].node.Path
	}
	v.rows = nil
	if j == nil {
		return
	}
	nodes, _ := journalTree(j.Items, "", nil)
	var walk func([]journalNode, string, int)
	walk = func(nodes []journalNode, lead string, depth int) {
		slices.SortStableFunc(nodes, func(a, b journalNode) int {
			rank := func(n journalNode) int {
				if n.Kind == "context" && !journalViewGroup(n) {
					return 0
				}
				if journalNodeClosed(n) {
					return 2
				}
				return 1
			}
			return rank(a) - rank(b)
		})
		for i, node := range nodes {
			row := journalPaneRow{node: node, lead: lead, depth: depth, last: i == len(nodes)-1}
			row.open = row.expandable()
			if expanded, explicit := v.expanded[node.Path]; explicit {
				row.open = row.expandable() && expanded
			}
			v.rows = append(v.rows, row)
			if !row.open {
				continue
			}
			// A child's branch sits under its parent's state glyph.
			next := lead + "│ "
			if depth == 0 || row.last {
				next = lead + "  "
			}
			walk(node.Children, next, depth+1)
		}
	}
	walk(nodes, "", 0)
	v.fit()
	for selected != "" && !slices.ContainsFunc(v.rows, func(row journalPaneRow) bool { return row.node.Path == selected }) {
		selected = journalParent(selected)
	}
	if index := slices.IndexFunc(v.rows, func(row journalPaneRow) bool { return row.node.Path == selected }); index >= 0 {
		v.selected = index
	}
	v.selected = max(0, min(v.selected, len(v.rows)-1))
}

// fit collapses the least recently updated visible subtrees first. A manual
// expansion protects its ancestors, so fitting never undoes a disclosure click.
func (v *nativeJournalView) fit() {
	if v.height <= 0 || len(v.rows) <= v.height {
		return
	}
	type candidate struct {
		path string
		at   time.Time
		seq  uint64
	}
	var latest func(journalNode) candidate
	latest = func(node journalNode) candidate {
		at, _ := time.Parse(time.RFC3339Nano, node.Updated.At)
		newest := candidate{path: node.Path, at: at, seq: node.Updated.Seq}
		for _, child := range node.Children {
			stamp := latest(child)
			if stamp.at.After(newest.at) {
				newest.at = stamp.at
			}
			newest.seq = max(newest.seq, stamp.seq)
		}
		return newest
	}
	var candidates []candidate
	for i, row := range v.rows {
		if !row.open {
			continue
		}
		if _, explicit := v.expanded[row.node.Path]; explicit {
			continue
		}
		pinned := false
		for k := i; k < len(v.rows) && (k == i || v.rows[k].depth > row.depth); k++ {
			node := v.rows[k].node
			if v.expanded[node.Path] {
				pinned = true
				break
			}
		}
		if !pinned {
			candidates = append(candidates, latest(row.node))
		}
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int {
		if order := a.at.Compare(b.at); order != 0 {
			return order
		}
		if a.seq < b.seq {
			return -1
		}
		if a.seq > b.seq {
			return 1
		}
		return 0
	})
	for _, candidate := range candidates {
		if len(v.rows) <= v.height {
			break
		}
		i := slices.IndexFunc(v.rows, func(row journalPaneRow) bool { return row.node.Path == candidate.path })
		if i < 0 || !v.rows[i].open {
			continue
		}
		end := i + 1
		for end < len(v.rows) && v.rows[end].depth > v.rows[i].depth {
			end++
		}
		v.rows[i].open = false
		v.rows = slices.Delete(v.rows, i+1, end)
	}
}

// The Agents group and mount diagnostics are view-only nodes at reserved keys.
// They carry the context kind but are not constraints.
func journalViewGroup(node journalNode) bool {
	_, key, _ := strings.CutLast(node.Path, "/")
	return node.Kind == "context" && strings.HasPrefix(key, "@")
}

func journalLocalTime(at string) string {
	stamp, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return ""
	}
	return stamp.Local().Format("15:04")
}

var journalStateOrder = []string{"working", "blocked", "pending", "done", "dropped"}

// journalStateGlyph colors a task state the way Activity colors agent states.
func journalStateGlyph(state string) string {
	glyph := journalGlyphs[state]
	switch state {
	case "working":
		return activityui.Amber + glyph + activityui.Reset
	case "blocked":
		return activityui.Red + glyph + activityui.Reset
	case "done":
		return activityui.Green + glyph + activityui.Reset
	case "":
		return " "
	}
	return activityui.Dim + glyph + activityui.Undim
}

// Header counts cover the presented journal's own tasks, in state order.
func journalStateCounts(items []journalItem, part func(state string, n int) string) []string {
	count := make(map[string]int)
	for _, item := range items {
		if item.Kind == "task" && !strings.Contains(item.Path, "/@") {
			count[item.State]++
		}
	}
	var parts []string
	for _, state := range journalStateOrder {
		if count[state] > 0 {
			parts = append(parts, part(state, count[state]))
		}
	}
	return parts
}

// journalTitleCounts styles the counts for the pane title, each state in its
// glyph color. Compact counts keep only the glyphs.
func journalTitleCounts(items []journalItem, compact bool) string {
	parts := journalStateCounts(items, func(state string, n int) string {
		if compact {
			return journalStateGlyph(state) + fmt.Sprintf(" %d", n)
		}
		return journalStateGlyph(state) + fmt.Sprintf(" %d ", n) + activityui.Dim + state + activityui.Undim
	})
	if compact {
		return strings.Join(parts, "  ")
	}
	return strings.Join(parts, activityui.Dim+" · "+activityui.Undim)
}

// journalPaneHints are the pane-local keys shown in its title; the status bar
// carries the rest.
func (u *appServerUI) journalPaneHints() string {
	hints := []string{"space expand", "d details"}
	if _, other := u.journalNamespaces(); other != "" {
		hints = append(hints, "n "+other)
	}
	for i, hint := range hints {
		key, label, _ := strings.Cut(hint, " ")
		hints[i] = "\x1b[1m" + key + "\x1b[22m " + activityui.Dim + label + activityui.Undim
	}
	return strings.Join(hints, activityui.Dim+" · "+activityui.Undim)
}

// render lays out the tree. header adds the state counts as a first row for a
// pane without a titled frame.
func (v *nativeJournalView) render(j *threadJournal, width, height int, header, focused bool, theme livediff.Theme) []string {
	rows := make([]string, max(0, height))
	v.top = 0
	if j != nil {
		if counts := journalTitleCounts(j.Items, width < 60); header && height >= 4 && counts != "" {
			rows[0] = ansi.Truncate(" "+counts, width, "…")
			v.top = 1
		}
	}
	body := height - v.top
	v.height = body
	v.rebuild(j)
	if height <= 0 {
		return rows
	}
	if len(v.rows) == 0 {
		rows[0] = activityui.Dim + " No journal entries." + activityui.Undim
		return rows
	}
	if v.reveal {
		if v.selected < v.offset {
			v.offset = v.selected
		}
		if v.selected >= v.offset+body {
			v.offset = v.selected - body + 1
		}
		v.reveal = false
	}
	v.offset = max(0, min(v.offset, len(v.rows)-body))
	marked := -1
	if hovered := v.offset + v.hover - 1 - v.top; v.hover > v.top && hovered < len(v.rows) {
		marked = hovered
	} else if v.cursor && focused {
		marked = v.selected
	}
	for i := v.offset; i < min(len(v.rows), v.offset+body); i++ {
		text := v.renderRow(v.rows[i], width, theme)
		if i == marked {
			fill := theme.SelectionBackground()
			text = strings.ReplaceAll(text, activityui.Reset, activityui.Reset+fill)
			text = fill + text + strings.Repeat(" ", max(0, width-ansi.StringWidth(text))) + "\x1b[49m"
		}
		rows[v.top+i-v.offset] = text
	}
	return rows
}

// renderRow is one tree row: guides, disclosure, state, text, and the local
// update time at the right edge when the pane is wide enough.
func (v *nativeJournalView) renderRow(row journalPaneRow, width int, theme livediff.Theme) string {
	node := row.node
	disclosure := "  "
	switch {
	case row.open:
		disclosure = "▾ "
	case row.expandable():
		disclosure = "▸ "
	case row.depth > 0 && row.last:
		disclosure = activityui.Dim + "└ " + activityui.Undim
	case row.depth > 0:
		disclosure = activityui.Dim + "├ " + activityui.Undim
	}
	dim := func(text string) string { return activityui.Dim + text + activityui.Undim }
	safe := func(text string) string { return livediff.Safe(text, false) }
	var text string
	switch node.Kind {
	case "task":
		_, key, _ := strings.CutLast(node.Path, "/")
		if node.Agent != "" && strings.HasPrefix(key, "@") {
			text = journalStateGlyph(node.State) + " " + journalAgentName(node.Agent, theme)
		} else {
			text = journalStateGlyph(node.State) + " " + dim(safe(journalDisplayPath(node))) + " " + safe(node.Title)
			if node.Agent != "" {
				text += dim(" ⎇ ") + journalAgentName(node.Agent, theme)
			}
		}
		if node.Reason != "" {
			reason := dim(safe(node.Reason))
			if node.State == "blocked" {
				reason = activityui.Amber + safe(node.Reason) + activityui.Reset
			}
			text += dim(" · ") + reason
		}
		if elapsed := journalTaskElapsed(node); elapsed != "" {
			text += dim(" · " + elapsed)
		}
	case "context":
		switch {
		case node.Path == "/@mount-error":
			text = activityui.Amber + "⚠ " + safe(node.Title) + activityui.Reset
		case journalViewGroup(node):
			text = dim("⎇ ") + "\x1b[1m" + safe(node.Title) + "\x1b[22m"
		default:
			text = theme.Accent() + "◆" + activityui.Reset + " " + dim(safe(journalDisplayPath(node))) + " " + safe(node.Title)
		}
	case "answer":
		first := journalPreview(node.Body)
		text = activityui.Green + "✓" + activityui.Reset + " \x1b[1mOutcome\x1b[22m"
		if first != "" {
			text += "  " + dim(safe(first))
		}
	default:
		title := node.Title
		if node.Kind == "note" && title == "Note" && node.Body != "" {
			title, _, _ = strings.Cut(node.Body, "\n")
		}
		text = dim("·") + " " + safe(title)
	}
	if row.expandable() && !row.open {
		text += dim(fmt.Sprintf(" +%d", journalDescendants(node)))
	}
	line := strings.Repeat(" ", journalRowMargin) + dim(row.lead) + disclosure + text
	if node.State == "dropped" {
		line = dim(ansi.Strip(line))
	}
	stamp := journalLocalTime(node.Updated.At)
	if stamp == "" || width < 40 {
		return ansi.Truncate(line, width, "…")
	}
	room := width - ansi.StringWidth(stamp) - 3
	line = ansi.Truncate(line, room, "…")
	return line + strings.Repeat(" ", max(0, room-ansi.StringWidth(line))) + " " + dim(stamp) + " "
}

func journalAgentName(agent string, theme livediff.Theme) string {
	color := activityui.Color(agent)
	if color == "" {
		color = "\x1b[1m" + theme.Accent()
	}
	return color + livediff.Safe(activityui.AgentDisplayName(agent), false) + activityui.Reset
}

// Reserved mount keys are durable addresses, not user-facing names. Keep the
// owning agent and its local ordinal without changing selection or copy targets.
func journalDisplayPath(node journalNode) string {
	_, mounted, ok := strings.CutLast(node.Path, "/@")
	if !ok {
		return node.Path
	}
	key, local, descendant := strings.Cut(mounted, "/")
	if key == "agents" {
		return "Agents"
	}
	if key == "mount-error" {
		return node.Title
	}
	agent := node.Author
	if !descendant {
		agent = node.Agent
	}
	label := activityui.AgentDisplayName(agent)
	if descendant {
		return strings.TrimSpace(label + " /" + local)
	}
	return label
}

func journalDescendants(node journalNode) int {
	n := len(node.Children)
	for _, child := range node.Children {
		n += journalDescendants(child)
	}
	return n
}

// journalTaskElapsed is a finished task's working time.
func journalTaskElapsed(node journalNode) string {
	if node.Started == nil || node.Finished == nil {
		return ""
	}
	start, firstErr := time.Parse(time.RFC3339Nano, node.Started.At)
	end, lastErr := time.Parse(time.RFC3339Nano, node.Finished.At)
	if firstErr != nil || lastErr != nil || end.Before(start) {
		return ""
	}
	return end.Sub(start).Round(time.Second).String()
}

// journalRowAt is the node row at a pane row, or -1.
func (v *nativeJournalView) journalRowAt(y int) int {
	if index := v.offset + y - v.top; y >= v.top && index < len(v.rows) {
		return index
	}
	return -1
}

func (u *terminalUI) journalKey(key string) error {
	view := &u.main.journalView
	view.rebuild(u.main.journalTreeSnapshot())
	switch key {
	case "j", "\x1b[B", "k", "\x1b[A", "\x1b[6~", "\x1b[5~", "g", "\x1b[H", "G", "\x1b[F":
		// A hidden cursor resumes in view, not wherever it was left.
		if !view.cursor && view.height > 0 {
			view.selected = max(view.offset, min(view.selected, view.offset+view.height-1))
		}
		view.cursor, view.reveal, view.hover = true, true, 0
	}
	if len(view.rows) == 0 {
		if key == "q" || key == "\x1b" {
			u.focus = 0
		}
		return nil
	}
	view.selected = max(0, min(view.selected, len(view.rows)-1))
	row := view.rows[view.selected]
	switch key {
	case "n":
		if _, other := u.main.journalNamespaces(); other != "" {
			view.unscoped = !view.unscoped
			view.selected, view.offset, view.expanded = 0, 0, nil
		}
	case "j", "\x1b[B":
		view.selected = min(len(view.rows)-1, view.selected+1)
	case "k", "\x1b[A":
		view.selected = max(0, view.selected-1)
	case "\x1b[6~":
		view.selected = min(len(view.rows)-1, view.selected+max(1, view.height))
	case "\x1b[5~":
		view.selected = max(0, view.selected-max(1, view.height))
	case "g", "\x1b[H":
		view.selected = 0
	case "G", "\x1b[F":
		view.selected = len(view.rows) - 1
	case " ":
		view.toggle(row)
	case "\x1b[C":
		if row.expandable() && !row.open {
			view.toggle(row)
		}
		view.cursor, view.reveal = true, true
	case "\x1b[D":
		if row.open {
			view.toggle(row)
		} else if parent := slices.IndexFunc(view.rows, func(other journalPaneRow) bool { return other.node.Path == journalParent(row.node.Path) }); parent >= 0 {
			view.selected = parent
		}
		view.cursor, view.reveal = true, true
	case "d":
		u.openJournalDetail(row.node)
	case "\r", "\n":
		u.openJournalRow(row.node)
	case "c":
		u.copyText(row.node.Path)
	case "q", "\x1b":
		u.focus = 0
	}
	return nil
}

func (v *nativeJournalView) toggle(row journalPaneRow) {
	if !row.expandable() {
		return
	}
	if v.expanded == nil {
		v.expanded = make(map[string]bool)
	}
	v.expanded[row.node.Path] = !row.open
}

// journalMouse points, scrolls, and clicks within the pane: the disclosure
// column toggles a subtree, and the rest of a row opens it. The wheel scrolls
// without moving the selection.
func (u *terminalUI) journalMouse(button, x, y int) error {
	view := &u.main.journalView
	view.rebuild(u.main.journalTreeSnapshot())
	switch button &^ 28 {
	case 64:
		view.offset = max(0, view.offset-3)
		return nil
	case 65:
		view.offset = max(0, min(view.offset+3, len(view.rows)-view.height))
		return nil
	}
	view.hover, view.cursor = y+1, false
	if button&^28 != 0 {
		return nil
	}
	index := view.journalRowAt(y)
	if index < 0 {
		return nil
	}
	view.selected = index
	row := view.rows[index]
	if from, to := row.disclosure(); row.expandable() && x >= from && x < to {
		view.toggle(row)
		return nil
	}
	u.openJournalRow(row.node)
	return nil
}

// clearJournalHover drops the pointer mark once the pointer leaves the pane.
func (u *terminalUI) clearJournalHover() {
	if u.main != nil {
		u.main.journalView.hover = 0
	}
}

// openJournalRow opens a mounted agent's Activity, or a node's details.
func (u *terminalUI) openJournalRow(node journalNode) {
	if node.Agent == "" {
		u.openJournalDetail(node)
		return
	}
	// An unresolved or not yet started agent has no Activity; the roster
	// would silently select another agent instead.
	if !slices.ContainsFunc(u.agents.feedAgents(), func(row liveActivityRosterRow) bool { return row.agent.Name == node.Agent }) {
		u.main.setNotice(node.Agent+" has no Activity yet.", false)
		return
	}
	u.openAgent(node.Agent)
}

func (u *terminalUI) openJournalDetail(node journalNode) {
	journal := u.main.journalTreeSnapshot()
	if journal == nil {
		return
	}
	roots, _ := journalTree(journal.Items, "", nil)
	var root *journalNode
	for i := range roots {
		if node.Path == roots[i].Path || strings.HasPrefix(node.Path, roots[i].Path+"/") {
			root = &roots[i]
			break
		}
	}
	if root == nil {
		return
	}
	var segments []activityui.Block
	target := 0
	var walk func(journalNode)
	walk = func(current journalNode) {
		if current.Path == node.Path {
			target = len(segments)
		}
		label := journalDisplayPath(current)
		if _, key, _ := strings.CutLast(current.Path, "/"); current.Agent != "" && strings.HasPrefix(key, "@") {
			current.Title = "" // The heading and display path already name this mount.
		}
		current.Path = label // Presentation copy only; navigation uses the original above.
		segments = append(segments, activityui.Block{Kind: "text", Verb: "Journal", Label: livediff.Safe(label, false),
			Body: livediff.Safe(strings.TrimSpace(journalEventText(journalEvent{Fields: current})), false)})
		for _, child := range current.Children {
			walk(child)
		}
	}
	walk(*root)
	u.openBlocks(u.main.view, []activityui.Block{{Kind: "text", Verb: "Journal", Label: livediff.Safe(root.Title, false)}})
	u.output.segments = segments
	u.output.pendingSegment, u.output.pendingFlash = target+1, node.Path != root.Path
}

// journalPin is the journal state the strip above the composer pins: the
// newest task change, shown with the task's current state.
type journalPin struct {
	node        journalNode
	done, total int
}

// journalPin reports the current owned task in the presented journal. A
// journal without an open task pins nothing once the turn ends.
func (u *appServerUI) journalPin() (journalPin, bool) {
	j := u.journalTreeSnapshot()
	if j == nil {
		return journalPin{}, false
	}
	var pin journalPin
	var current *journalItem
	var finished *journalItem
	priority := func(state string) int {
		switch state {
		case "working":
			return 0
		case "blocked":
			return 1
		}
		return 2
	}
	for i := range j.Items {
		item := &j.Items[i]
		if item.Kind != "task" || strings.Contains(item.Path, "/@") {
			continue // Mounted roots and their descendants belong to child journals.
		}
		pin.total++
		if item.State == "done" || item.State == "dropped" {
			pin.done++
			if finished == nil || item.Updated > finished.Updated {
				finished = item
			}
			continue
		}
		if current == nil || priority(item.State) < priority(current.State) || priority(item.State) == priority(current.State) && item.Updated > current.Updated {
			current = item
		}
	}
	if current == nil && u.turn != "" {
		current = finished
	}
	if current == nil {
		return journalPin{}, false
	}
	pin.node = current.node()
	return pin, true
}

// journalPlanStrip pins current work above the composer, with owned progress.
func (u *appServerUI) journalPlanStrip(width int) string {
	pin, ok := u.journalPin()
	if !ok {
		return ""
	}
	theme := u.view.painter.Theme
	node := pin.node
	node.Body = "" // The strip identifies work; supporting detail belongs in the card.
	left := journalNodeRow(theme, journalCompactNode(node), "")
	if pin.node.State == "working" && pin.node.Started != nil {
		if start, err := time.Parse(time.RFC3339Nano, pin.node.Started.At); err == nil {
			left += activityui.Dim + " · " + time.Since(start).Round(time.Second).String() + activityui.Undim
		}
	}
	// The pane key yields first, then progress, before the task's title.
	progress := fmt.Sprintf("%d/%d done", pin.done, pin.total)
	for _, right := range []string{progress + " · Ctrl-B 5 journal", progress} {
		if room := width - ansi.StringWidth(right) - 2; ansi.StringWidth(left) <= room || right == progress && room >= 24 {
			left = ansi.Truncate(left, room, "…")
			return left + strings.Repeat(" ", width-ansi.StringWidth(left)-ansi.StringWidth(right)) + activityui.Dim + right + activityui.Undim
		}
	}
	return ansi.Truncate(left, width, "…")
}

var journalGlyphs = map[string]string{"pending": "○", "working": "◐", "done": "●", "blocked": "⚠", "dropped": "⊘"}

// journalStateColor colors a task's glyph by state, as Activity colors
// outcomes: working in the accent, done green, blocked amber.
func journalStateColor(theme livediff.Theme, state string) string {
	switch state {
	case "working":
		return theme.Accent()
	case "done":
		return activityui.Green
	case "blocked":
		return activityui.Amber
	}
	return activityui.Dim
}

// journalEventVerb names the change an event made to its task.
func journalEventVerb(event journalEvent) string {
	switch {
	case event.Op == "remove":
		return "removed"
	case event.Fields.Kind != "task":
		return ""
	case event.Fields.State == "working":
		return "started"
	case event.Fields.State == "done", event.Fields.State == "blocked", event.Fields.State == "dropped":
		return event.Fields.State
	case event.Op == "add":
		return "added"
	case event.Transition:
		return "reopened"
	}
	return "updated"
}

// journalNodeRow is one node on one styled row: a state glyph, the dim path,
// the title, then dim details. verb names the change a transcript row shows.
func journalNodeRow(theme livediff.Theme, node journalNode, verb string) string {
	lead, text := journalNodeParts(theme, node, verb)
	return lead + text
}

// journalNodeParts splits a node row into its glyph lead and its text, so
// wrapped rows can hang under the text.
func journalNodeParts(theme livediff.Theme, node journalNode, verb string) (lead, text string) {
	safe := func(text string) string { return livediff.Safe(strings.Join(strings.Fields(text), " "), false) }
	title := safe(node.Title)
	path := safe(journalDisplayPath(node))
	switch {
	case verb == "removed":
		return activityui.Dim + "⊖ " + activityui.Undim, activityui.Dim + path + " " + title + " · removed" + activityui.Undim
	case node.Kind == "note":
		if title == "Note" && node.Body != "" {
			first, _, _ := strings.Cut(node.Body, "\n")
			title = safe(first)
		}
		return theme.Accent() + "◆" + activityui.Reset + " ", title
	case node.Kind != "task":
		return activityui.Dim + "◇ " + activityui.Undim, activityui.Dim + path + activityui.Undim + " " + title
	}
	color := journalStateColor(theme, node.State)
	var details []string
	if verb != "" {
		details = append(details, verb)
	}
	if node.Reason != "" {
		details = append(details, safe(node.Reason))
	}
	if elapsed := journalTaskElapsed(node); elapsed != "" {
		details = append(details, elapsed)
	}
	if node.State == "dropped" {
		title = activityui.Dim + title + activityui.Undim
	}
	text = activityui.Dim + path + activityui.Undim + " " + title
	if len(details) > 0 {
		style := activityui.Dim
		if node.State == "blocked" {
			style = activityui.Amber
		}
		text += style + " · " + strings.Join(details, " · ") + activityui.Reset
	}
	return color + journalGlyphs[node.State] + activityui.Reset + " ", text
}

func (v *liveActivityView) applyTreeJournal(thread string, p nativeJournalPublication) {
	kind := "journal_event"
	text := p.item.Text
	if p.event != nil {
		if stamp := journalLocalTime(p.event.At); stamp != "" {
			text = stamp + "  " + text
		}
	}
	if p.card != nil {
		kind = "journal_card"
	}
	entry := activityPaneEntry{Seq: v.lastSeq + 1, Agent: "Main", Kind: kind, Text: text, Observed: v.now(), journalCard: p.card, journalEvent: p.event,
		native: &liveActivityNativeItem{thread: thread, turn: "journal-v2", item: p.item.ID, phase: kind}}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
}

// journalCardLines lays out Main's work report separately from its ordinary
// answer, with its notes counted; the card opens as journalCardBlock.
func (v *liveActivityView) journalCardLines(out *conversationLines, entry activityPaneEntry, width int) {
	card := entry.journalCard
	if card == nil {
		return
	}
	p := &v.painter
	snippet := liveActivitySnippet{run: entry.Seq, block: 0}
	rows, facts := journalCardRows(p, card, max(1, width-4), false)
	title := activityui.Green + "✓" + activityui.Reset + activityui.Dim + " journal"
	if len(facts) > 0 {
		title += " · " + strings.Join(facts, " · ")
	}
	title += activityui.Undim
	if width < 12 {
		rows = append([]string{title}, rows...)
	} else {
		rows = activityui.Card(title, entry.Observed.Local().Format("15:04"), rows, width, false)
	}
	for i, row := range rows {
		out.add(0, row)
		if i > 0 {
			out.snippets[len(out.snippets)-1] = snippet
		}
	}
}

// journalCardBlock retains full reasons and bodies for the dialog and copy.
func (v *liveActivityView) journalCardBlock(entry activityPaneEntry) activityui.Block {
	card := entry.journalCard
	p := &v.painter
	changed, _, open := journalCardEntries(card.Journal, card.Since)
	facts := journalCardFacts(changed, open, true)
	facts = append([]string{entry.Observed.Local().Format("15:04")}, facts...)
	return activityui.Block{Kind: "text", Verb: "Journal", Source: entry.Seq,
		Body:   journalTurnCard(card.Journal, card.Since, false),
		Detail: activityui.Dim + strings.Join(facts, " · ") + activityui.Undim,
		Rows: func(width int) []string {
			rows, _ := journalCardRows(p, card, width, true)
			return rows
		}}
}

// Counts do not depend on width or Markdown rendering.
func journalCardFacts(changed []journalEvent, open int, includeNotes bool) []string {
	done, notes := 0, 0
	for _, event := range changed {
		if event.Op == "remove" {
			continue
		}
		if event.Fields.Kind == "task" && event.Fields.State == "done" {
			done++
		} else if event.Fields.Kind == "note" {
			notes++
		}
	}
	var facts []string
	if done > 0 {
		facts = append(facts, fmt.Sprintf("%d done", done))
	}
	if open > 0 {
		facts = append(facts, fmt.Sprintf("%d open", open))
	}
	if includeNotes && notes > 0 {
		label := "1 note"
		if notes > 1 {
			label = fmt.Sprintf("%d notes", notes)
		}
		facts = append(facts, label)
	}
	return facts
}

const journalCompletedCardRows = 8 // Body budget; with borders, a small card is at most ten rows.

// journalCardRows lays out a work report's body: what
// happened this turn one node per row, then the tasks still open. This turn
// aggregates each node to its final state in the window; a node both added
// and removed within it is omitted from the report, not the history. Collapsed
// rows preview the newest notes; expanded rows retain all bodies and reasons.
// facts are the title's counts, including changed open tasks shown only once.
func journalCardRows(p *activityui.Painter, card *nativeJournalCard, inner int, expand bool) (rows, facts []string) {
	theme := p.Theme
	changed, left, open := journalCardEntries(card.Journal, card.Since)
	var happened [][]string
	row := func(node journalNode, verb string) []string {
		if !expand {
			node = journalCompactNode(node)
		}
		lead, text := journalNodeParts(theme, node, verb)
		lines := activityui.Hang(lead, text, inner)
		if !expand && len(lines) > journalCardPreviewRows {
			lines = lines[:journalCardPreviewRows]
			lines[len(lines)-1] = ansi.Truncate(lines[len(lines)-1], max(1, inner-1), "") + "…"
		}
		if expand && verb != "removed" {
			body := node.Body
			if node.Kind == "note" && node.Title == "Note" {
				_, body, _ = strings.Cut(body, "\n") // The row already shows its first line.
			}
			if body = strings.TrimSpace(body); body != "" {
				for _, line := range p.Markdown(livediff.Safe(body, false), max(1, inner-2)) {
					lines = append(lines, "  "+line)
				}
			}
		}
		return lines
	}
	for _, event := range changed {
		switch {
		case event.Op == "remove":
			happened = append(happened, row(event.Fields, "removed"))
		case event.Fields.Kind == "note" && !expand:
			// Preview the newest notes together after the task changes.
		default:
			happened = append(happened, row(event.Fields, ""))
		}
	}
	if !expand {
		notes := journalCardNotes(changed)
		for _, note := range notes[max(0, len(notes)-journalCardNoteLimit):] {
			happened = append(happened, row(note.Fields, ""))
		}
		if hidden := len(notes) - journalCardNoteLimit; hidden > 0 {
			label := fmt.Sprintf("%d earlier notes", hidden)
			if hidden == 1 {
				label = "1 earlier note"
			}
			happened = append(happened, activityui.Hang(theme.Accent()+"◆"+activityui.Reset+" ", label+activityui.Dim+" · click to open"+activityui.Undim, inner))
		}
	}
	var remaining [][]string
	for _, node := range left {
		remaining = append(remaining, row(node, ""))
	}
	section := func(label string, items [][]string) {
		if len(items) == 0 {
			return
		}
		if len(rows) > 0 {
			rows = append(rows, "")
		}
		if label != "" {
			rows = append(rows, "\x1b[1m"+label+"\x1b[22m")
		}
		for _, item := range items {
			rows = append(rows, item...)
		}
	}
	section("This turn", happened)
	if card.Journal.mountUnavailable != "" {
		section("", [][]string{activityui.Hang(activityui.Amber+"⚠ "+activityui.Reset, activityui.Amber+"Mounted journals unavailable: "+livediff.Safe(card.Journal.mountUnavailable, false)+activityui.Reset, inner)})
	}
	section("Remaining", remaining)
	if len(rows) == 0 {
		rows = []string{activityui.Dim + "No journal changes this turn; no open tasks." + activityui.Undim}
	}
	// Measure the actual preview after wrapping, not the number of tasks. Keep
	// open work and mount diagnostics visible; the full dialog is never collapsed.
	if !expand && open == 0 && card.Journal.mountUnavailable == "" && len(rows) > journalCompletedCardRows {
		return activityui.Hang("▸ ", "Details"+activityui.Dim+" · click to open"+activityui.Undim, inner), journalCardFacts(changed, open, true)
	}
	return rows, journalCardFacts(changed, open, expand)
}

// journalEventsItem shows adjacent journal changes as one journal item: a
// heading, then each task change or note on its own row under the journal
// gutter. A row's time shows only where it differs from the row above.
func (v *liveActivityView) journalEventsItem(out *conversationLines, first, last, width int) []activityui.Block {
	p := &v.painter
	accent := p.Theme.Accent()
	head := v.entries[first].activityPaneEntry
	out.add(0, conversationHeading(accent+"◆"+activityui.Reset, "\x1b[1m"+accent+"journal"+activityui.Reset, "", head, width))
	gutter := accent + "│" + activityui.Reset + " "
	body := max(1, width-2)
	var laid []activityui.Block
	stamp := head.Observed.Local().Format("15:04")
	for k := first; k <= last; k++ {
		entry := v.entries[k].activityPaneEntry
		if !v.visible(entry) || entry.Kind != "journal_event" {
			continue
		}
		lead, text := "", livediff.Safe(entry.Text, false)
		detail := ""
		if event := entry.journalEvent; event != nil {
			lead, text = journalNodeParts(p.Theme, event.Fields, journalEventVerb(*event))
			if event.Op != "remove" && strings.TrimSpace(event.Fields.Body) != "" {
				detail = journalEventText(*event)
				text += activityui.Dim + " ›" + activityui.Undim
			}
			if at := journalLocalTime(event.At); at != "" && at != stamp {
				stamp = at
				if gap := body - ansi.StringWidth(lead+text) - len(at); gap >= 2 {
					text += strings.Repeat(" ", gap) + activityui.Dim + at + activityui.Undim
				}
			}
		}
		snippet := liveActivitySnippet{}
		if detail != "" {
			snippet = liveActivitySnippet{run: head.Seq, block: len(laid)}
			laid = append(laid, activityui.Block{Kind: "text", Verb: "Journal", Label: entry.journalEvent.Path, Body: livediff.Safe(detail, false)})
		}
		// Wrapped rows hang under the text, past the state glyph.
		for _, line := range activityui.Hang(lead, text, body) {
			out.add(0, gutter+line)
			out.snippets[len(out.snippets)-1] = snippet
		}
	}
	return laid
}
