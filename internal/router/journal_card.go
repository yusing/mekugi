package router

import (
	"cmp"
	"crypto/rand"
	jsonv1 "encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

const journalCardNoteLimit = 3
const journalCardPreviewRows = 2

// Single-line previews retain inline styles; block summaries keep their existing geometry.
func journalInlinePreview(p *activityui.Painter, text string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	plain := journalPreview(text)
	styled := p.Inline(first)
	if strings.TrimSpace(ansi.Strip(styled)) == plain {
		return styled
	}
	return plain
}

func journalPreview(text string) string {
	// Summary strips styles. Keep Markdown geometry without discarded syntax work.
	p := activityui.Painter{LayoutOnly: true}
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	// Ordinary rows are independent of later content. Block constructs still
	// need their full input, including table lookahead and quoted fences.
	if _, fenced := activityui.FenceDelimiter(first); !fenced && !strings.HasPrefix(first, ">") && !strings.Contains(first, "|") {
		if summary := p.Summary([]activityui.Block{{Kind: "final", Body: first}}, 80); summary != "" {
			return summary
		}
	}
	return p.Summary([]activityui.Block{{Kind: "final", Body: text}}, 80)
}

// Compact presentation never changes the retained node or its full details.
func journalCompactNode(node journalNode) journalNode {
	return journalCompactNodePreview(node, journalPreview)
}

func journalCompactNodePreview(node journalNode, preview func(string) string) journalNode {
	if node.Reason != "" {
		node.Reason = ansi.Truncate(preview(node.Reason), 80, "…")
	}
	if node.Kind == "note" || node.Kind == "context" || node.Kind == "task" && node.Body != "" {
		if node.Title == "Note" && node.Body != "" {
			node.Title = preview(node.Body)
		} else {
			node.Title = preview(node.Title)
			if node.Body != "" {
				node.Title += ": " + preview(node.Body)
			}
		}
	}
	node.Title = ansi.Truncate(node.Title, 160, "…")
	return node
}

// Main's native and inline reports derive one final change per owned path.
// Child completion keeps its separate event-delta contract.
func journalCardEntries(j threadJournal, since uint64) (changed []journalEvent, remaining []journalNode, open int) {
	var paths []string
	latest := make(map[string]journalEvent)
	added := make(map[string]bool)
	for _, event := range j.Events {
		if event.Seq <= since || j.LegacyFlush[event.Seq] || event.Fields.Kind == "answer" {
			continue
		}
		if _, seen := latest[event.Path]; !seen {
			paths = append(paths, event.Path)
			added[event.Path] = event.Op == "add"
		}
		latest[event.Path] = event
	}
	shown := make(map[string]bool)
	for _, path := range paths {
		event := latest[path]
		if event.Op == "remove" && added[path] {
			continue
		}
		removedParent := false
		for parent := journalParent(path); parent != ""; parent = journalParent(parent) {
			if latest[parent].Op == "remove" {
				removedParent = true
				break
			}
		}
		if !removedParent {
			changed = append(changed, event)
			shown[path] = true
		}
	}
	for _, item := range j.Items {
		if item.Kind != "task" || journalNodeClosed(item.node()) || strings.Contains(item.Path, "/@") {
			continue
		}
		open++
		if !shown[item.Path] {
			remaining = append(remaining, item.node())
		}
	}
	return changed, remaining, open
}

func journalCardNotes(events []journalEvent) []journalEvent {
	var notes []journalEvent
	for _, event := range events {
		if event.Op != "remove" && event.Fields.Kind == "note" {
			notes = append(notes, event)
		}
	}
	slices.SortFunc(notes, func(a, b journalEvent) int { return cmp.Compare(a.Seq, b.Seq) })
	return notes
}

func journalTaskText(node journalNode) string {
	glyph := journalGlyphs[node.State]
	text := fmt.Sprintf("%s %s %s", glyph, node.Path, node.Title)
	if node.Reason != "" {
		text += " · " + node.Reason
	}
	if elapsed := journalTaskElapsed(node); elapsed != "" {
		text += " · " + elapsed
	}
	return text + journalSupersededText(node)
}

func journalSupersededText(node journalNode) string {
	if node.SupersededBy == "" {
		return ""
	}
	return " · superseded by " + node.SupersededBy
}

func journalEventText(event journalEvent) string {
	if event.Op == "remove" {
		return "Removed " + event.Path + " " + event.Fields.Title
	}
	node := event.Fields
	if node.Kind == "answer" || event.Legacy {
		return node.Body
	}
	if node.Kind == "task" {
		text := journalTaskText(node)
		if node.Body != "" {
			text += "\n\n" + node.Body
		}
		return text
	}
	text := node.Title + journalSupersededText(node)
	if node.Body != "" {
		text += "\n\n" + node.Body
	}
	return text
}

// Answer capture and unchanged open tasks alone are not a Main work report.
func journalHasReport(j threadJournal, since uint64) bool {
	if j.mountUnavailable != "" {
		return true
	}
	changed, _, _ := journalCardEntries(j, since)
	return len(changed) > 0
}

func journalTurnCard(j threadJournal, since uint64, child bool) string {
	if !child {
		return journalMainCard(j, since, true)
	}
	// A child's recipient already knows its author: Codex names the agent on
	// both the completion notification and the inter-agent result.
	var text strings.Builder
	entries := 0
	for _, event := range j.Events {
		if event.Seq <= since || event.Fields.Kind != "answer" {
			continue
		}
		text.WriteString("\n\n" + journalEventText(event))
		entries++
	}
	happened := false
	for _, event := range j.Events {
		if event.Seq <= since || event.Fields.Kind == "answer" {
			continue
		}
		if !happened {
			text.WriteString("\n")
		}
		happened = true
		entries++
		text.WriteString("\n- " + strings.TrimPrefix(indentJournalText(journalEventText(event), "  "), "  "))
	}
	// The event delta can omit tasks from an earlier turn. Surface their
	// current state at handoff without changing child-owned work or replaying
	// old events. Changed open tasks already appear in the delta above.
	_, remaining, _ := journalCardEntries(j, since)
	if len(remaining) > 0 {
		text.WriteString("\n\n**Remaining**")
		for _, node := range remaining {
			text.WriteString("\n- " + strings.TrimPrefix(indentJournalText(journalEventText(journalEvent{Fields: node}), "  "), "  "))
		}
	}
	if entries == 0 && len(remaining) == 0 {
		return "No new journal entries."
	}
	return strings.TrimLeft(text.String(), "\n")
}

func journalMainCard(j threadJournal, since uint64, expand bool) string {
	changed, remaining, _ := journalCardEntries(j, since)
	var notes []journalEvent
	entries := changed
	if !expand {
		notes = journalCardNotes(changed)
		entries = slices.DeleteFunc(slices.Clone(changed), func(event journalEvent) bool {
			return event.Fields.Kind == "note" && event.Op != "remove"
		})
		entries = append(entries, notes[max(0, len(notes)-journalCardNoteLimit):]...)
	}
	var text strings.Builder
	text.WriteString("Journal")
	started := false
	for _, event := range entries {
		if !started {
			text.WriteString("\n\n**This turn**")
			started = true
		}
		if !expand {
			event.Fields = journalCompactNode(event.Fields)
			event.Fields.Body = ""
		}
		text.WriteString("\n- " + strings.TrimPrefix(indentJournalText(journalEventText(event), "  "), "  "))
	}
	if hidden := len(notes) - journalCardNoteLimit; !expand && hidden > 0 {
		label := "earlier notes"
		if hidden == 1 {
			label = "earlier note"
		}
		text.WriteString(fmt.Sprintf("\n- %d %s; read the journal for full details.", hidden, label))
	}
	if j.mountUnavailable != "" {
		text.WriteString("\n\nMounted journals unavailable: " + j.mountUnavailable)
	}
	if len(remaining) > 0 {
		text.WriteString("\n\n**Remaining**")
		for _, node := range remaining {
			if !expand {
				node = journalCompactNode(node)
				node.Body = ""
			}
			text.WriteString("\n- " + strings.TrimPrefix(indentJournalText(journalEventText(journalEvent{Fields: node}), "  "), "  "))
		}
	}
	if !strings.Contains(text.String(), "\n") {
		text.WriteString("\n\nNo journal changes this turn; no open tasks.")
	}
	return text.String()
}

// Inline delivery uses the same durable event window as the native card.
func (t *mekugiResponseTransform) prepareTreeDelivery(j threadJournal, terminal bool) ([]map[string]jsonv1.RawMessage, error) {
	since := j.LiveSeq
	text := ""
	sequence := uint64(0)
	if terminal {
		since = j.FlushSeq
		if !journalHasReport(j, since) {
			t.ReleaseDelivery()
			return nil, nil
		}
		text = journalMainCard(j, since, false)
		sequence = j.Sequence
	} else {
		var output strings.Builder
		header := "Journal"
		output.WriteString(header)
		for _, event := range j.Events {
			if event.Seq <= since || j.LegacyLive[event.Seq] || event.Fields.Kind == "answer" {
				continue
			}
			row := "\n- " + strings.TrimPrefix(indentJournalText(journalEventText(event), "  "), "  ")
			// A row no update can hold would block every later event; durable reads keep it whole.
			const clipped = "\n  … (clipped; read the journal for full details)"
			if limit := maxCommentaryPublicationBytes - len(header); len(row) > limit {
				row = strings.ToValidUTF8(row[:limit-len(clipped)], "") + clipped
			}
			if output.Len()+len(row) > maxCommentaryPublicationBytes-t.journalLiveBytes {
				break
			}
			output.WriteString(row)
			sequence = event.Seq
		}
		text = output.String()
	}
	if !terminal && (sequence == 0 || sequence <= since) {
		t.ReleaseDelivery()
		return nil, nil
	}
	if len(text) > maxJournalFlushBytes {
		t.ReleaseDelivery()
		return nil, fmt.Errorf("journal card exceeds terminal capacity")
	}
	identity := fmt.Sprintf("%t:%d", terminal, sequence)
	if terminal {
		if t.journalResponseID == "" {
			t.journalResponseID = rand.Text()
		}
		identity += ":" + t.journalResponseID
	}
	id := commentaryMessageID("journal-v2\x00" + t.directory + "\x00" + t.shellThreadID + "\x00" + identity)
	message := assistantCommentaryMessage(id, text)
	if terminal && !t.journalAnswerStarted && (!t.journalNaturalFinalSeen || len(t.journalNaturalAnswerIDs) != 0) {
		message["phase"] = mustMarshalJSON("final_answer")
	}
	retained := t.retainCommentary(message)
	if len(retained) == 0 {
		t.ReleaseDelivery()
		return nil, fmt.Errorf("cannot retain journal delivery")
	}
	if t.journalDeliveries == nil {
		t.journalDeliveries = make(map[string]journalDelivery)
	}
	t.journalDeliveries[id] = journalDelivery{thread: t.shellThreadID, terminal: terminal, sequence: sequence, tree: true}
	if !terminal {
		t.journalLiveBytes += len(text)
	}
	return retained, nil
}
