package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
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
			if block.Journal != nil {
				for _, group := range block.Journal.Groups {
					for _, answer := range group.Answers {
						targets[answer.ID] = group.Target
					}
				}
			}
		}
	}
	item := publication.item
	// A terminal flush acknowledges milestones without moving their existing
	// live transcript position past the work they preceded.
	if publication.terminal && !item.TerminalOnly && item.Question == "" {
		for _, previous := range v.entries {
			if previous.native != nil && previous.native.thread == thread && previous.native.turn == "journal" && previous.journal != nil && previous.journal.ID == item.ID {
				publication.terminal = false
				break
			}
		}
	}
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
			if block.Journal == nil {
				continue
			}
			for j := range block.Journal.Groups {
				group := &block.Journal.Groups[j]
				for _, answer := range group.Answers {
					if target := targets[answer.ID]; target != 0 {
						group.Target = target
						break
					}
				}
				if group.Target != 0 || group.Question == "" {
					continue
				}
				for _, question := range slices.Backward(v.entries[:i]) {
					if question.Agent == "You" && question.Text == group.Question {
						group.Target = question.Seq
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
	wait               *activityui.Block // Structured wait progress is an aside in Main.
	command, status    string
	searchResults      *int
	running            bool               // Started live and not yet completed; replay never sets it.
	collapseAt         time.Time          // A settled live block stays open until then.
	collapsed          bool               // A settled block shows collapsed: after its linger, or restored.
	images             []composerImage    // Attachment spans, not text resembling image labels.
	question           uint64             // Original user entry, retained even for a live journal publication.
	thought            time.Duration      // Reasoning time from its first summary delta to completion.
	attachments        []activityui.Block // Submitted file snapshot outcomes, recovered from host history.
}

func (n *liveActivityNativeItem) sameItem(other *liveActivityNativeItem) bool {
	return other != nil && n.thread == other.thread && n.turn == other.turn && n.item == other.item
}

func (v *liveActivityView) entrySeq(entry activityPaneEntry) uint64 {
	for _, current := range v.entries {
		if entry.native != nil && entry.native.sameItem(current.native) || entry.native == nil && current.Seq == entry.Seq {
			return current.Seq
		}
	}
	return 0
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
	if text, wait, handled := appServerProgress(item, method); handled {
		if text != "" {
			entry.Kind, entry.Text = "progress", text
			entry.native.wait = wait
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
		}
		return
	}
	if method == "item/agentMessage/delta" {
		entry.Text = delta
	} else {
		switch item.Type {
		case "reasoning":
			entry.Kind, entry.CallID, entry.Text = "reasoning", id, strings.Join(item.Summary, "\n\n")
			if strings.TrimSpace(entry.Text) == "" {
				return
			}
		case "agentMessage":
		case "userMessage":
			var ok bool
			if entry.Text, entry.native.images, ok = appServerUserText(item.Content); !ok {
				return
			}
			if thread == main {
				entry.Agent = "You"
			} else {
				entry.Agent += " · user"
			}
		case "commandExecution":
			entry.Kind = "command"
		default:
			return // In particular, never render raw/encrypted reasoning.
		}
	}
	v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	if item.Type == "userMessage" {
		if blocks := appServerAttachmentBlocks(item.Content); len(blocks) > 0 {
			if thread == main {
				entry.Agent = "Main"
			} else {
				entry.Agent = "Thread " + thread
			}
			entry.Seq, entry.Kind, entry.Text = v.lastSeq+1, "attachments", ""
			entry.native = &liveActivityNativeItem{thread: thread, turn: turn, item: id + "/attachments", phase: method, attachments: blocks}
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
		}
	}
}

// appServerUserText renders userMessage content as the composer wrote it,
// with each image as its "[Image N]" label.
func appServerUserText(content jsontext.Value) (string, []composerImage, bool) {
	var contents []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if len(content) > 0 && json.Unmarshal(content, &contents) != nil {
		return "", nil, false
	}
	var text string
	var images []composerImage
	for _, content := range contents {
		if content.Type == "text" {
			if _, attached := decodeFileAttachments(content.Text); attached {
				continue
			}
			text += content.Text
		} else if content.Type == "image" || content.Type == "localImage" {
			start := len(text)
			text += fmt.Sprintf("[Image %d]", len(images)+1)
			images = append(images, composerImage{start: start, end: len(text)})
		}
	}
	return text, images, true
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
		entry.native.question = previous.native.question
		blocks := parseLiveActivity(entry)
		for _, annotation := range v.blocks[i] {
			if annotation.Kind == "filter" {
				blocks = append(blocks, annotation)
			}
		}
		v.entries[i], v.blocks[i] = entry, blocks
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
	previousAnswers := make(map[string]*activityui.Answer)
	for i, entry := range v.entries[:index] {
		if entry.Agent != owner {
			continue
		}
		for _, block := range v.blocks[i] {
			if block.Journal != nil {
				for _, group := range block.Journal.Groups {
					for j, answer := range group.Answers {
						known[answer.ID] = group.Target
						previousAnswers[answer.ID] = &group.Answers[j]
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
		if block.Journal == nil {
			continue
		}
		var groups []activityui.AnswerGroup
		for _, group := range block.Journal.Groups {
			var current uint64
			if group.Question != "" {
				for _, entry := range slices.Backward(v.entries[:index]) {
					if (entry.Kind == "assignment" || entry.Kind == "start") && entry.assignment != nil && entry.assignment.id != "" && entry.assignment.to == owner && normalize(entry.assignment.text) == group.Question ||
						entry.Kind == "start" && entry.Agent == owner && strings.Contains(entry.Text, "\nSpawn assignment:\n") && normalize(activityui.ParseStart(entry.Text).Body) == group.Question {
						current = entry.Seq
						break
					}
				}
			}
			first := len(groups)
			for _, answer := range group.Answers {
				target, seen := known[answer.ID]
				if previous := previousAnswers[answer.ID]; previous != nil {
					previous.Text = answer.Text
					continue // A cumulative snapshot updates, rather than repeats, an earlier answer.
				}
				if !seen {
					target = current
					known[answer.ID] = target
				}
				if len(groups) == first || groups[len(groups)-1].Target != target {
					groups = append(groups, activityui.AnswerGroup{Question: group.Question, Target: target})
				}
				last := &groups[len(groups)-1]
				last.Answers = append(last.Answers, answer)
			}
		}
		block.Journal.Groups = groups
	}
	v.runs = nil
}
