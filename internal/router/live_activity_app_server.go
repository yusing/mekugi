package router

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func (v *liveActivityView) applyJournal(thread string, publication nativeJournalPublication) {
	// Keep question links stable across live-to-terminal replacement and edits,
	// including when an identical prompt is submitted again later.
	targets := make(map[string]uint64)
	for i, previous := range v.entries {
		if previous.native == nil || previous.native.thread != thread {
			continue
		}
		if previous.journal != nil && previous.native.question != 0 {
			targets[previous.journal.ID] = previous.native.question
		}
		for _, block := range v.blocks[i] {
			if block.journal != nil {
				for _, group := range block.journal.groups {
					for _, answer := range group.answers {
						targets[answer.id] = group.target
					}
				}
			}
		}
	}
	item := publication.item
	entry := activityPaneEntry{Seq: v.lastSeq + 1, Agent: "Main", Kind: "native_journal", Text: item.Text, Observed: time.Now(), journal: &item,
		native: &liveActivityNativeItem{thread: thread, turn: "journal", item: item.ID, phase: "journal"}}
	if targets[item.ID] == 0 && item.Question != "" {
		for _, question := range slices.Backward(v.entries) {
			if question.Agent == "You" && question.Text == item.Question {
				targets[item.ID] = question.Seq
				break
			}
		}
	}
	entry.native.question = targets[item.ID]
	if !publication.terminal {
		entry.Kind = "text"
	} else if !publication.retracted {
		entry.native.turn, entry.native.item = "journal-terminal", fmt.Sprint(publication.batch)
		entry.journalItems = []journalItem{item}
	}
	// Replace prior live/terminal occurrences of this exact journal ID. A
	// terminal batch stays one ordinary Activity journal-result block. A
	// retraction removes every occurrence and adds nothing in its place.
	for i := len(v.entries) - 1; i >= 0; i-- {
		previous := &v.entries[i]
		if previous.native == nil || previous.native.thread != thread || !publication.retracted && entry.native.sameItem(previous.native) {
			continue
		}
		if previous.journal == nil {
			continue
		}
		if len(previous.journalItems) > 0 {
			previous.journalItems = slices.DeleteFunc(previous.journalItems, func(other journalItem) bool { return other.ID == item.ID })
			if len(previous.journalItems) > 0 {
				previous.journal = &previous.journalItems[0]
				v.blocks[i] = parseLiveActivity(*previous)
				v.runs = nil
				continue
			}
		} else if previous.journal.ID != item.ID {
			continue
		}
		v.entries = slices.Delete(v.entries, i, i+1)
		v.blocks = slices.Delete(v.blocks, i, i+1)
		v.runs = nil
	}
	// A retraction re-parses surviving batch members, so it also relinks them.
	if !publication.retracted {
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	}
	for i, current := range v.entries {
		if current.native == nil || current.native.thread != thread {
			continue
		}
		for _, block := range v.blocks[i] {
			if block.journal == nil {
				continue
			}
			for j := range block.journal.groups {
				group := &block.journal.groups[j]
				for _, answer := range group.answers {
					if target := targets[answer.id]; target != 0 {
						group.target = target
						break
					}
				}
				if group.target != 0 || group.question == "" {
					continue
				}
				for _, question := range slices.Backward(v.entries[:i]) {
					if question.Agent == "You" && question.Text == group.question {
						group.target = question.Seq
						break
					}
				}
			}
		}
	}
}

// Native items extend the activity model rather than creating another transcript
// cache. Their identities are deliberately not joined to provider call IDs.
type liveActivityNativeItem struct {
	thread, turn, item string
	phase              string
	command, status    string
	question           uint64 // Original user entry, retained even for a live journal publication.
}

func (n *liveActivityNativeItem) sameItem(other *liveActivityNativeItem) bool {
	return other != nil && n.thread == other.thread && n.turn == other.turn && n.item == other.item
}

func (v *liveActivityView) applyAppServerItem(main, thread, turn, id, method, delta string, item appServerItem) {
	if thread == "" || turn == "" || id == "" {
		return
	}
	entry := activityPaneEntry{Seq: v.lastSeq + 1, Agent: "Main", Kind: "text", Text: item.Text, Observed: time.Now(),
		native: &liveActivityNativeItem{thread: thread, turn: turn, item: id, phase: method, command: item.Command, status: item.Status}}
	if thread != main {
		entry.Agent = "Thread " + thread
	}
	if method == "item/agentMessage/delta" {
		entry.Text = delta
	} else {
		switch item.Type {
		case "agentMessage":
		case "userMessage":
			if thread == main {
				entry.Agent = "You"
			} else {
				entry.Agent += " · user"
			}
			imageNumber := 0
			for _, content := range item.Content {
				if content.Type == "text" {
					entry.Text += content.Text
				} else if content.Type == "image" || content.Type == "localImage" {
					imageNumber++
					entry.Text += fmt.Sprintf("[Image %d]", imageNumber)
				}
			}
		case "commandExecution":
			entry.Kind = "command"
		default:
			return // In particular, never render raw/encrypted reasoning.
		}
	}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
}

// mergeNative updates the owning activity entry in place. Completed snapshots
// supersede fragments; duplicate starts and late deltas cannot reopen them.
func (v *liveActivityView) mergeNative(entry activityPaneEntry) bool {
	if entry.native == nil {
		return false
	}
	for i, previous := range v.entries {
		if previous.native != nil && previous.native.phase == "input/pending" && entry.Agent == "You" && entry.native.thread == previous.native.thread && entry.Text == previous.Text {
			// Reconcile the local echo with Codex's authoritative user item.
			entry.Seq, entry.Observed = previous.Seq, previous.Observed
			v.entries[i], v.blocks[i], v.runs = entry, parseLiveActivity(entry), nil
			return true
		}
		if !entry.native.sameItem(previous.native) {
			continue
		}
		if previous.native.phase == "item/completed" || entry.native.phase == "item/started" {
			return true
		}
		if entry.native.phase == "item/agentMessage/delta" {
			entry.Text = previous.Text + entry.Text
		}
		if len(entry.journalItems) > 0 {
			items := slices.Clone(previous.journalItems)
			for _, item := range entry.journalItems {
				index := slices.IndexFunc(items, func(other journalItem) bool { return other.ID == item.ID })
				if index < 0 {
					items = append(items, item)
				} else {
					items[index] = item
				}
			}
			slices.SortFunc(items, func(a, b journalItem) int {
				if a.Created < b.Created {
					return -1
				}
				if a.Created > b.Created {
					return 1
				}
				return 0
			})
			entry.journalItems = items
		}
		entry.Seq, entry.Observed = previous.Seq, previous.Observed
		v.entries[i], v.blocks[i] = entry, parseLiveActivity(entry)
		v.runs = nil
		return true
	}
	return false
}

func (v *liveActivityView) removePendingInput(seq uint64) {
	for i, entry := range v.entries {
		if entry.Seq == seq && entry.native != nil && entry.native.phase == "input/pending" {
			v.entries = slices.Delete(v.entries, i, i+1)
			v.blocks = slices.Delete(v.blocks, i, i+1)
			v.runs = nil
			return
		}
	}
}

// Child completions contain cumulative journal snapshots. Bind each answer ID
// once, preserving older answers' assignment targets through later follow-ups.
func (v *liveActivityView) linkChildAnswers(seq uint64) {
	index := slices.IndexFunc(v.entries, func(entry activityPaneEntry) bool { return entry.Seq == seq })
	if index < 0 {
		return
	}
	owner := v.entries[index].Agent
	known := make(map[string]uint64)
	for i, entry := range v.entries[:index] {
		if entry.Agent != owner {
			continue
		}
		for _, block := range v.blocks[i] {
			if block.journal != nil {
				for _, group := range block.journal.groups {
					for _, answer := range group.answers {
						known[answer.id] = group.target
					}
				}
			}
		}
	}
	// Match the journal renderer's presentation copy, never modify source text.
	normalize := func(text string) string {
		return strings.Trim(livediff.Safe(indentJournalText(text, ""), false), "\n")
	}
	for _, block := range v.blocks[index] {
		if block.journal == nil {
			continue
		}
		var groups []liveActivityAnswerGroup
		for _, group := range block.journal.groups {
			var current uint64
			if group.question != "" {
				for _, entry := range slices.Backward(v.entries[:index]) {
					if (entry.Kind == "assignment" || entry.Kind == "start") && entry.assignment != nil && entry.assignment.id != "" && entry.assignment.to == owner && normalize(entry.assignment.text) == group.question ||
						entry.Kind == "start" && entry.Agent == owner && strings.Contains(entry.Text, "\nSpawn assignment:\n") && normalize(parseLiveActivityStart(entry.Text).body) == group.question {
						current = entry.Seq
						break
					}
				}
			}
			first := len(groups)
			for _, answer := range group.answers {
				target, seen := known[answer.id]
				if !seen {
					target = current
					known[answer.id] = target
				}
				if len(groups) == first || groups[len(groups)-1].target != target {
					groups = append(groups, liveActivityAnswerGroup{question: group.question, target: target})
				}
				last := &groups[len(groups)-1]
				last.answers = append(last.answers, answer)
			}
		}
		block.journal.groups = groups
	}
	v.runs = nil
}
