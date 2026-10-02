package router

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Journal presentation is not part of Codex's item history. Reconstruct it
// from retained revisions and actual host turn boundaries, not live delivery
// cursors. Reading this timeline neither publishes nor acknowledges anything.
type journalReplayPublication struct {
	at          time.Time
	turn        string
	workspace   string
	publication nativeJournalPublication
	tree        *threadJournal
}

type journalReplayTurn struct {
	id, status string
	start, end time.Time
	items      []appServerItem
	inline     bool
}

func journalReplayInlineTurns(store *mekugiReplayStore, workspace string, turns []journalReplayTurn) []journalReplayTurn {
	turns = slices.Clone(turns)
	for i := range turns {
		for _, item := range turns[i].items {
			if item.Type != "agentMessage" || !strings.HasPrefix(strings.TrimSpace(item.Text), "Journal") {
				continue
			}
			if _, found, err := store.read(workspace, item.ID, true); err == nil && found {
				turns[i].inline = true
				break
			}
		}
	}
	return turns
}

func journalReplayTimeline(j threadJournal, turns []journalReplayTurn) []journalReplayPublication {
	if !j.TreeAuthored {
		return nil
	}
	var result []journalReplayPublication
	snapshot := threadJournal{Version: 2, TreeAuthored: true, Author: j.Author, LegacyFlush: j.LegacyFlush}
	next := 0
	since := uint64(0)
	baseline := false
	for _, turn := range turns {
		start, end := turn.start, turn.end
		if start.IsZero() || end.IsZero() {
			continue // Missing timing is not evidence of a completion window.
		}
		for next < len(j.Events) {
			event := j.Events[next]
			at, err := time.Parse(time.RFC3339Nano, event.At)
			if err != nil {
				next++
				continue
			}
			if at.After(end) {
				break
			}
			next++
			if !baseline && !at.Before(start) {
				copy := snapshot.clone()
				result = append(result, journalReplayPublication{at: start, workspace: j.Workspace, tree: &copy})
				baseline = true
			}
			applyJournalReplayEvent(&snapshot, event)
			if at.Before(start) && !baseline {
				since = event.Seq // Inherited or omitted turns are not a local report window.
				continue
			}
			text := journalRowText(event)
			if turn.inline || event.Fields.Kind == "answer" || j.LegacyLive[event.Seq] || event.Op == "set" && !event.Transition {
				text = ""
			}
			result = append(result, journalReplayPublication{at: at, turn: turn.id, workspace: j.Workspace,
				publication: nativeJournalPublication{event: &event, item: journalItem{ID: fmt.Sprintf("event:%d", event.Seq), Updated: event.Seq, Text: text, Author: event.Author}}})
		}
		if !baseline {
			copy := snapshot.clone()
			result = append(result, journalReplayPublication{at: start, workspace: j.Workspace, tree: &copy})
			baseline = true
		}
		// Only an acknowledged report and a successful host turn establish a
		// historical completion. Failed/interrupted turns never gain a card.
		if turn.status == "completed" && snapshot.Sequence <= j.FlushSeq && journalHasReport(snapshot, since) {
			card := snapshot.clone()
			card.Events = slices.DeleteFunc(card.Events, func(event journalEvent) bool { return event.Seq <= since })
			card.Items = slices.DeleteFunc(card.Items, func(item journalItem) bool { return item.Kind != "task" || journalNodeClosed(item.node()) })
			if !turn.inline {
				result = append(result, journalReplayPublication{at: end, turn: turn.id, workspace: j.Workspace,
					publication: nativeJournalPublication{terminal: true, card: &nativeJournalCard{Journal: card, Since: since},
						item: journalItem{ID: "card:replay:" + turn.id, Updated: snapshot.Sequence, Author: j.Author}}})
			}
			since = snapshot.Sequence
		}
	}
	// A reset or other retained revision can follow the last completed turn
	// before its continuation starts. It is presentation, not a completion.
	if baseline {
		for _, event := range j.Events[next:] {
			at, err := time.Parse(time.RFC3339Nano, event.At)
			if err != nil {
				continue
			}
			text := journalRowText(event)
			if event.Fields.Kind == "answer" || j.LegacyLive[event.Seq] || event.Op == "set" && !event.Transition {
				text = ""
			}
			result = append(result, journalReplayPublication{at: at, workspace: j.Workspace,
				publication: nativeJournalPublication{event: &event, item: journalItem{ID: fmt.Sprintf("event:%d", event.Seq), Updated: event.Seq, Text: text, Author: event.Author}}})
		}
	}
	return result
}

func applyJournalReplayEvent(j *threadJournal, event journalEvent) {
	j.Sequence = event.Seq
	j.Events = append(j.Events, event)
	if event.Op == "remove" {
		j.Items = slices.DeleteFunc(j.Items, func(item journalItem) bool {
			return item.Path == event.Path || strings.HasPrefix(item.Path, event.Path+"/")
		})
		return
	}
	item := event.Fields.item()
	if index := j.treeIndex(event.Path); index >= 0 {
		j.Items[index] = item
	} else {
		j.Items = append(j.Items, item)
	}
}

func (u *appServerUI) restoredJournalPlacements(turns []appServerHistoryTurn, itemAt map[string]time.Time) []*restoredPlacement {
	if u.proxy == nil || u.proxy.replayStore == nil {
		return nil
	}
	var placements []*restoredPlacement
	var windows []journalReplayTurn
	for _, turn := range turns {
		window := journalReplayTurn{id: turn.ID, status: turn.Status, start: historyTime(turn.StartedAt), end: historyTime(turn.CompletedAt), items: turn.Items}
		// Host history uses whole seconds; rollout item stamps retain precision.
		if !window.end.IsZero() {
			window.end = window.end.Add(time.Second - time.Nanosecond)
		}
		for _, item := range turn.Items {
			at := itemAt[item.ID]
			if window.start.IsZero() && !at.IsZero() {
				window.start = at
			}
			if at.After(window.end) {
				window.end = at
			}
		}
		if turn.Status == "inProgress" {
			window.end = u.now()
		}
		if u.restoring != nil {
			if precise, found := u.restoring.journalTurns[turn.ID]; found && !precise.start.IsZero() && !precise.end.IsZero() {
				window.start, window.end = precise.start, precise.end
			}
		}
		windows = append(windows, window)
	}
	for i := range len(windows) - 1 {
		if !windows[i+1].start.After(windows[i].end) {
			// Whole-second host timestamps cannot partition overlapping turns.
			// Keep revisions visible, but do not invent either completion card.
			windows[i].end = windows[i+1].start.Add(-time.Nanosecond)
			windows[i].status, windows[i+1].status = "", ""
		}
	}
	for _, workspace := range slices.Compact([]string{u.session.cwd, ""}) {
		j, exists, err := readThreadJournal(u.proxy.replayStore, workspace, u.thread)
		if err != nil {
			u.setNotice("Journal history: "+err.Error(), true)
			continue
		}
		if !exists {
			continue
		}
		view := newLiveActivityView()
		for _, update := range journalReplayTimeline(j, journalReplayInlineTurns(u.proxy.replayStore, workspace, windows)) {
			p := update.publication
			if update.tree != nil || p.event != nil && p.item.Text == "" {
				continue
			}
			if p.event != nil && p.event.Op == "reset" {
				p.recovery = u.journalResetRecovery(workspace, u.thread, p.event.ResetTurn)
			}
			view.applyJournal(journalKey(workspace, u.thread), p)
			entry := view.entries[len(view.entries)-1].activityPaneEntry
			entry.Observed = update.at
			placement := &restoredPlacement{entry: entry, at: update.at}
			if p.card != nil {
				placement.turn = update.turn
				if index := slices.IndexFunc(turns, func(turn appServerHistoryTurn) bool { return turn.ID == update.turn }); index >= 0 && len(turns[index].Items) > 0 {
					placement.anchor = turns[index].Items[len(turns[index].Items)-1].ID
				}
			}
			placements = append(placements, placement)
		}
	}
	return placements
}
