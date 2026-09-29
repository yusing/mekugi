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
	unscoped                 bool
	expanded                 map[string]bool
	selected, offset, height int
	top                      int // Header rows above the first node row.
	rows                     []journalPaneRow
}

type journalPaneRow struct {
	node   journalNode
	indent int
}

func (u *appServerUI) journalTreeSnapshot() *threadJournal {
	if sink := u.selectedJournalSink(); sink != nil {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		if sink.mounted != nil {
			return sink.mounted
		}
		return sink.tree
	}
	return nil
}

func (u *appServerUI) selectedJournalSink() *nativeJournalSink {
	sinks := []*nativeJournalSink{u.journal, u.unscopedJournal}
	if u.journalView.unscoped {
		slices.Reverse(sinks)
	}
	for _, sink := range sinks {
		if sink == nil {
			continue
		}
		sink.mu.Lock()
		tree := sink.tree
		if sink.mounted != nil {
			tree = sink.mounted
		}
		sink.mu.Unlock()
		if tree != nil {
			return sink
		}
	}
	return nil
}

// The last layout alone is stale when a key closed the pane since that paint;
// a suppressed note would then show nowhere and still be acknowledged.
func (u *appServerUI) journalPanePresents(sink *nativeJournalSink) bool {
	s := u.shell
	return s != nil && s.journalOpen && s.focus != 1 && s.focus != 2 && !s.diffOpen && s.layout.journal.w > 0 && s.output == nil && u.selectedJournalSink() == sink
}

func (v *nativeJournalView) rebuild(j *threadJournal) {
	v.rows = nil
	if j == nil {
		return
	}
	nodes, _ := journalTree(j.Items, "", nil)
	var walk func([]journalNode, int)
	walk = func(nodes []journalNode, indent int) {
		slices.SortStableFunc(nodes, func(a, b journalNode) int {
			rank := func(n journalNode) int {
				if n.Kind == "context" && !journalViewGroup(n) {
					return 0
				}
				if n.State == "done" || n.State == "dropped" {
					return 2
				}
				return 1
			}
			return rank(a) - rank(b)
		})
		for _, node := range nodes {
			v.rows = append(v.rows, journalPaneRow{node, indent})
			closed := node.State == "done" || node.State == "dropped"
			if !closed || v.expanded[node.Path] {
				walk(node.Children, indent+1)
			}
		}
	}
	walk(nodes, 0)
	v.selected = max(0, min(v.selected, len(v.rows)-1))
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

// Header counts cover the presented journal's own tasks.
func journalStateCounts(items []journalItem) string {
	count := make(map[string]int)
	for _, item := range items {
		if item.Kind == "task" && !strings.Contains(item.Path, "/@") {
			count[item.State]++
		}
	}
	var parts []string
	for _, state := range []string{"working", "blocked", "pending", "done", "dropped"} {
		if count[state] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", state, count[state]))
		}
	}
	return strings.Join(parts, " · ")
}

func (v *nativeJournalView) render(j *threadJournal, width, height int) []string {
	v.rebuild(j)
	v.height = height
	rows := make([]string, max(0, height))
	if height == 0 {
		return rows
	}
	if len(v.rows) == 0 {
		rows[0] = activityui.Dim + "No journal entries." + activityui.Undim
		return rows
	}
	v.top = 0
	if counts := journalStateCounts(j.Items); height >= 4 && counts != "" {
		rows[0] = activityui.Dim + ansi.Truncate(counts, width, "…") + activityui.Undim
		rows[1] = activityui.Dim + strings.Repeat("─", width) + activityui.Undim
		v.top = 2
	}
	body := height - v.top
	v.height = body
	v.offset = max(0, min(v.offset, len(v.rows)-body))
	if v.selected < v.offset {
		v.offset = v.selected
	}
	if v.selected >= v.offset+body {
		v.offset = v.selected - body + 1
	}
	for i := v.offset; i < min(len(v.rows), v.offset+body); i++ {
		row := v.rows[i]
		node := row.node
		text := node.Title
		switch node.Kind {
		case "task":
			text = journalTaskText(node)
			if node.Agent != "" {
				text = "⎇ " + text
				if _, key, _ := strings.CutLast(node.Path, "/"); strings.HasPrefix(key, "@") {
					text = "⎇ " + node.Agent
					if node.State != "" {
						text += " · " + node.State
					}
					if node.Reason != "" {
						text += " · " + node.Reason
					}
				}
			}
			if len(node.Children) > 0 && (node.State == "done" || node.State == "dropped") {
				if v.expanded[node.Path] {
					text = "▾ " + text
				} else {
					text = "▸ " + text
				}
			}
		case "context":
			if node.Path == "/@mount-error" {
				text = "⚠ " + text
			} else if journalViewGroup(node) {
				text = "⎇ " + text
			} else {
				text = "◆ " + node.Path + " " + text
			}
		case "answer":
			text = "Outcome"
		}
		if node.Kind == "note" && node.Title == "Note" && node.Body != "" {
			text, _, _ = strings.Cut(node.Body, "\n")
		}
		stamp := journalLocalTime(node.Updated.At)
		if stamp != "" {
			text = stamp + "  " + text
		}
		text = ansi.Truncate(strings.Repeat("  ", row.indent)+livediff.Safe(text, false), width, "…")
		if node.State == "blocked" {
			text = activityui.Amber + text + activityui.Reset
		} else if node.State == "dropped" {
			text = activityui.Dim + text + activityui.Undim
		}
		if i == v.selected {
			text = "\x1b[7m" + text + "\x1b[27m"
		}
		rows[v.top+i-v.offset] = text
	}
	return rows
}

func (u *terminalUI) journalKey(key string) error {
	view := &u.main.journalView
	view.rebuild(u.main.journalTreeSnapshot())
	switch key {
	case "n":
		view.unscoped = !view.unscoped
		view.selected, view.offset, view.expanded = 0, 0, nil
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
		view.selected = max(0, len(view.rows)-1)
	case " ", "\x1b[C", "\x1b[D":
		if len(view.rows) > 0 {
			node := view.rows[max(0, view.selected)].node
			if node.State != "done" && node.State != "dropped" || len(node.Children) == 0 {
				u.openJournalDetail(node)
				break
			}
			path := node.Path
			if view.expanded == nil {
				view.expanded = make(map[string]bool)
			}
			view.expanded[path] = !view.expanded[path]
		}
	case "d":
		if len(view.rows) > 0 {
			u.openJournalDetail(view.rows[max(0, view.selected)].node)
		}
	case "\r", "\n":
		if len(view.rows) > 0 {
			node := view.rows[max(0, view.selected)].node
			// An unresolved or not yet started agent has no Activity; the roster
			// would silently select another agent instead.
			known := node.Agent != "" && slices.ContainsFunc(u.agents.feedAgents(), func(row liveActivityRosterRow) bool { return row.agent.Name == node.Agent })
			if node.Agent != "" && !known {
				u.main.setNotice(node.Agent+" has no Activity yet.", false)
			} else if known {
				u.pushNavigationReturn()
				u.focus, u.journalOpen, u.diffOpen, u.activityOpen = 2, false, false, true
				u.agents.selected, u.agents.only, u.agents.following = node.Agent, true, true
			} else {
				u.copyText(node.Path)
			}
		}
	case "q", "\x1b":
		u.focus = 0
	}
	return nil
}

func (u *terminalUI) openJournalDetail(node journalNode) {
	text := journalEventText(journalEvent{Fields: node})
	pages := []activityui.Block{{Kind: "text", Verb: "Journal", Label: node.Path, Body: livediff.Safe(text, false)}}
	u.selection = nil
	u.output = &outputDialog{view: u.main.view, origins: pages, pages: pages, match: -1}
	u.output.showPage(0)
}

func (u *appServerUI) journalPlanStrip(width int) string {
	j := u.journalTreeSnapshot()
	if j == nil {
		return ""
	}
	var current, next *journalItem
	done, total := 0, 0
	for i := range j.Items {
		item := &j.Items[i]
		if item.Kind != "task" {
			continue
		}
		if strings.Contains(item.Path, "/@") {
			continue // Mounted roots and their descendants belong to child journals.
		}
		total++
		if item.State == "done" || item.State == "dropped" {
			done++
			continue
		}
		if current == nil || item.State == "working" && current.State != "working" || item.State == "blocked" && current.State == "pending" {
			current = item
		}
	}
	if current == nil {
		return ""
	}
	for i := range j.Items {
		if item := &j.Items[i]; item != current && item.Kind == "task" && item.State == "pending" && !strings.Contains(item.Path, "/@") {
			next = item
			break
		}
	}
	elapsed := ""
	if current.State == "working" && current.Started != nil {
		if start, err := time.Parse(time.RFC3339Nano, current.Started.At); err == nil {
			elapsed = " · " + time.Since(start).Round(time.Second).String()
		}
	}
	upcoming := ""
	if next != nil {
		upcoming = "  ▸ " + next.Path + " " + next.Title
	}
	text := fmt.Sprintf("%s · %d/%d done%s%s  (Ctrl-B 5: journal)", journalTaskText(current.node()), done, total, elapsed, upcoming)
	return ansi.Truncate(livediff.Safe(text, false), width, "…")
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
	entry := activityPaneEntry{Seq: v.lastSeq + 1, Agent: "Main", Kind: kind, Text: text, Observed: time.Now(), journalCard: p.card, journalEvent: p.event,
		native: &liveActivityNativeItem{thread: thread, turn: "journal-v2", item: p.item.ID, phase: kind}}
	if p.card != nil {
		for _, event := range p.card.Journal.Events {
			if event.Seq <= p.card.Since || event.Fields.Kind != "answer" {
				continue
			}
			for _, question := range slices.Backward(v.entries) {
				if journalQuestionMatches(question, event.Fields.Question) {
					entry.native.question = question.Seq
					break
				}
			}
		}
	}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
}

func (v *liveActivityView) journalCardLines(out *conversationLines, entry activityPaneEntry, width int) {
	card := entry.journalCard
	if card == nil {
		return
	}
	snippet := liveActivitySnippet{run: entry.Seq, block: 0}
	body := journalTurnCard(card.Journal, card.Since, false)
	if !v.expanded[snippet] {
		// The Outcome leads. This turn aggregates each node to its final state in
		// the window; a node both added and removed within it never happened.
		var lines, paths []string
		latest := make(map[string]journalEvent)
		added := make(map[string]bool)
		for _, event := range card.Journal.Events {
			if event.Seq <= card.Since || card.Journal.LegacyFlush[event.Seq] {
				continue
			}
			if event.Fields.Kind == "answer" {
				lines = append(lines, journalEventText(event))
				continue
			}
			if _, seen := latest[event.Path]; !seen {
				paths = append(paths, event.Path)
				added[event.Path] = event.Op == "add"
			}
			latest[event.Path] = event
		}
		var happened []string
		notes := 0
		for _, path := range paths {
			event := latest[path]
			switch {
			case event.Op == "remove" && added[path]:
			case event.Op == "remove":
				happened = append(happened, journalEventText(event))
			case event.Fields.Kind == "task":
				happened = append(happened, journalTaskText(event.Fields))
			default:
				notes++
			}
		}
		if notes == 1 {
			happened = append(happened, "1 note · enter to expand")
		} else if notes > 1 {
			happened = append(happened, fmt.Sprintf("%d notes · enter to expand", notes))
		}
		if len(happened) > 0 {
			lines = append(lines, "This turn  "+strings.Join(happened, " · "))
		}
		if card.Journal.mountUnavailable != "" {
			lines = append(lines, "Mounted journals unavailable: "+card.Journal.mountUnavailable)
		}
		var remaining []string
		for _, item := range card.Journal.Items {
			if item.Kind == "task" && item.State != "done" && item.State != "dropped" {
				remaining = append(remaining, journalTaskText(item.node()))
			}
		}
		if len(remaining) > 0 {
			lines = append(lines, "Remaining  "+strings.Join(remaining, " · "))
		}
		body = strings.Join(lines, "\n\n")
	}
	if entry.native.question != 0 {
		for _, question := range v.entries {
			if question.Seq == entry.native.question {
				v.replyContext(out, question, mainGutter(&v.painter), max(1, width-2))
				break
			}
		}
	}
	text := v.painter.Markdown(livediff.Safe(body, false), max(1, width-4))
	rows := nativeBox(width, len(text)+2, "Journal", entry.Observed.Local().Format("15:04"), false, text, nil)
	for _, row := range rows {
		out.add(0, row)
		out.snippets[len(out.snippets)-1] = snippet
	}
}
