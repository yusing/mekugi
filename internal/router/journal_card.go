package router

import (
	"crypto/rand"
	jsonv1 "encoding/json"
	"fmt"
	"strings"
	"time"
)

func journalTaskText(node journalNode) string {
	glyph := map[string]string{"pending": "○", "working": "◐", "done": "●", "blocked": "⚠", "dropped": "⊘"}[node.State]
	text := fmt.Sprintf("%s %s %s", glyph, node.Path, node.Title)
	if node.Reason != "" {
		text += " · " + node.Reason
	}
	if node.Started != nil && node.Finished != nil {
		start, firstErr := time.Parse(time.RFC3339Nano, node.Started.At)
		end, lastErr := time.Parse(time.RFC3339Nano, node.Finished.At)
		if firstErr == nil && lastErr == nil && !end.Before(start) {
			text += " · " + end.Sub(start).Round(time.Second).String()
		}
	}
	return text
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
	text := node.Title
	if node.Body != "" {
		text += "\n\n" + node.Body
	}
	return text
}

func journalTurnCard(j threadJournal, since uint64, child bool) string {
	// A child's recipient already knows its author: Codex names the agent on
	// both the completion notification and the inter-agent result.
	var text strings.Builder
	if !child {
		text.WriteString("Journal")
	}
	entries := 0
	for _, event := range j.Events {
		if event.Seq <= since || !child && j.LegacyFlush[event.Seq] || event.Fields.Kind != "answer" {
			continue
		}
		text.WriteString("\n\n" + journalEventText(event))
		entries++
	}
	happened := false
	for _, event := range j.Events {
		if event.Seq <= since || !child && j.LegacyFlush[event.Seq] || event.Fields.Kind == "answer" {
			continue
		}
		if !happened {
			if child {
				text.WriteString("\n")
			} else {
				text.WriteString("\n\n**This turn**")
			}
		}
		happened = true
		entries++
		text.WriteString("\n- " + strings.TrimPrefix(indentJournalText(journalEventText(event), "  "), "  "))
	}
	if !child {
		if j.mountUnavailable != "" {
			text.WriteString("\n\nMounted journals unavailable: " + j.mountUnavailable)
		}
		remaining := false
		for _, item := range j.Items {
			if item.Kind != "task" || item.State == "done" || item.State == "dropped" {
				continue
			}
			if !remaining {
				text.WriteString("\n\n**Remaining**")
				remaining = true
			}
			text.WriteString("\n- " + journalTaskText(item.node()))
		}
	}
	if child {
		if entries == 0 {
			return "No new journal entries."
		}
		return strings.TrimLeft(text.String(), "\n")
	}
	if !strings.Contains(text.String(), "\n") {
		// An empty Outcome with nothing new or open still ends the turn visibly.
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
		text = journalTurnCard(j, since, false)
		sequence = j.Sequence
	} else {
		var output strings.Builder
		header := "Journal update " + commentaryCode(j.Author)
		output.WriteString(header)
		for _, event := range j.Events {
			if event.Seq <= since || j.LegacyLive[event.Seq] || event.Fields.Kind == "answer" {
				continue
			}
			row := "\n- " + strings.TrimPrefix(indentJournalText(journalEventText(event), "  "), "  ")
			// A row no update can hold would block every later event; the card keeps it whole.
			const clipped = "\n  … (clipped; the turn card shows it in full)"
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
	if terminal {
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
