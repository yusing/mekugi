package router

import (
	"cmp"
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxJournalSummaryBytes    = 64 << 10
	maxJournalSummaryFailures = 8
)

type journalSummary struct {
	Text              string
	Changes, Failures int
}

// journalExecutions reads immutable execution evidence owned by the selected
// session. It never searches model prose for status, nor reads the workspace.
// Called under store.lock; a failed evidence read cannot mean no failures.
func (s *mekugiReplayStore) journalExecutions(workspace, thread string) ([]replayRecord, error) {
	session, err := s.readRetainedSession(storageSessionName(thread))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []replayRecord
	for _, name := range slices.Sorted(maps.Keys(session.Files)) {
		if !strings.HasPrefix(name, "call-") {
			continue
		}
		data, err := readManagedOutputFile(filepath.Join(s.directory, name))
		if err != nil {
			return nil, err
		}
		var record replayRecord
		// Match the durable writer's numeric-array command output hashes.
		if err := json.Unmarshal(data, &record, jsonv1.FormatByteArrayAsArray(true)); err != nil {
			return nil, err
		}
		if record.Workspace != workspace || record.History.ExecutingThread != thread {
			continue
		}
		if (record.Version != 1 && record.Version != 2) || replayRecordName(record.Workspace, record.CallID, record.Commentary) != name {
			return nil, errors.New("invalid journal execution evidence identity")
		}
		history := record.History
		if history.ExecObservation == nil && history.ExecOutcome == nil {
			continue
		}
		// Recovery needs execution facts, not every retained file baseline and
		// review diff accumulated by the thread. Do not keep those in memory.
		record.History = mekugiHistory{Script: history.Script, Report: history.Report, ExecOutcome: history.ExecOutcome}
		if history.ExecObservation != nil {
			record.History.ExecObservation = &execObservation{CodeMode: history.ExecObservation.CodeMode}
		}
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b replayRecord) int {
		return cmp.Or(cmp.Compare(a.CaptureOrder, b.CaptureOrder), strings.Compare(a.CallID, b.CallID))
	})
	return records, nil
}

func summaryExcerpt(text string, limit int, notice string) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "… " + notice
}

// summaryTail keeps the end of command output, where failures usually report.
func summaryTail(text string, limit int, notice string) string {
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return notice + " …" + text[start:]
}

// journalSummaryLocked renders deterministically from durable facts only. The
// mandatory context and open tasks must fit; otherwise synthesis must fall back
// to the provider rather than silently discarding a constraint or resume target.
func (s *mekugiReplayStore) journalSummaryLocked(ctx context.Context, j threadJournal) (journalSummary, error) {
	return s.renderJournalSummaryLocked(ctx, j, 0)
}

// journalSummaryBoundedLocked uses UTF-16 code units, matching JavaScript
// string.length. It preserves mandatory facts in full or returns no text. Like
// journalSummaryLocked, the caller owns the journal and replay-store locks.
func (s *mekugiReplayStore) journalSummaryBoundedLocked(ctx context.Context, j threadJournal, maxCharacters int) (journalSummary, error) {
	if maxCharacters <= 0 {
		return journalSummary{}, errors.New("journal summary character capacity must be positive")
	}
	return s.renderJournalSummaryLocked(ctx, j, maxCharacters)
}

func journalSummaryCharacters(text string) int {
	n := 0
	for _, r := range text {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

// A zero character limit selects the existing byte-budget policy. Both carriers
// select and render the same durable facts; only admission and full open-task
// bodies differ. Optional sections are admitted atomically, never packet-clipped.
func (s *mekugiReplayStore) renderJournalSummaryLocked(ctx context.Context, j threadJournal, maxCharacters int) (journalSummary, error) {
	bounded := maxCharacters > 0
	measure := func(text string) int { return len(text) }
	capacity := maxJournalSummaryBytes
	if bounded {
		measure, capacity = journalSummaryCharacters, maxCharacters
	}
	var result journalSummary
	if !j.IdentityKnown || j.IdentityConflicted {
		return result, errors.New("journal summary identity is unavailable or conflicted")
	}
	journals, recordErrors, err := newJournalStore().workspaceJournals(s, j.Workspace)
	if err != nil {
		return result, err
	}
	items, err := mountedJournalItems(journals, recordErrors, j.Thread, j.Thread)
	if err != nil {
		return result, err
	}
	// A completed child does not complete the parent's integration task. Keep
	// both facts adjacent so recovery does not mistake it for an active review.
	completedAgents := make(map[string]string)
	for _, item := range items {
		parent, key, _ := strings.CutLast(item.Path, "/")
		if item.Agent != "" && item.State == "done" && strings.HasPrefix(key, "@") {
			completedAgents[parent] = item.Path
		}
	}
	// A superseded node is history, not current fact: one pointer line replaces
	// its body and its descendants, so recovery cannot revive a replaced decision.
	superseded := make(map[string]bool)
	for _, item := range items {
		if item.SupersededBy != "" {
			superseded[item.Path] = true
		}
	}
	hiddenBySupersession := func(path string) bool {
		for parent := journalParent(path); parent != ""; parent = journalParent(parent) {
			if superseded[parent] {
				return true
			}
		}
		return false
	}
	openTasks := make(map[string]bool)
	for _, item := range items {
		if item.Kind == "task" && item.State != "done" && item.State != "dropped" {
			openTasks[item.Path] = true
		}
	}
	// Closed work is an index, not a replay of its result bodies and history.
	// Keep a finished agent's latest answer inline only when its directly bound
	// integration task is still open. All details remain readable by path.
	collapsed := make(map[string]string) // collapsing path -> kept answer path
	finishedAgents := make(map[string]bool)
	for _, item := range items {
		_, key, _ := strings.CutLast(item.Path, "/")
		switch {
		case item.Kind == "task" && (item.State == "done" || item.State == "dropped") && item.SupersededBy == "":
			collapsed[item.Path] = ""
			if item.Agent != "" && strings.HasPrefix(key, "@") && openTasks[journalParent(item.Path)] {
				finishedAgents[item.Path] = true
			}
		case item.Kind == "answer" && finishedAgents[journalParent(item.Path)]:
			// Answers are root nodes in creation order; the last is the latest.
			collapsed[journalParent(item.Path)] = item.Path
		}
	}
	// The outermost collapsing ancestor is the visible line that folds path.
	// An open task bounds folding: its work stays actionable even under a
	// dropped task or a finished agent.
	collapsedBy := func(path string) (owner string) {
		for parent := journalParent(path); parent != "" && !openTasks[parent]; parent = journalParent(parent) {
			if kept, ok := collapsed[parent]; ok && kept != path {
				owner = parent
			}
		}
		return owner
	}
	var text strings.Builder
	text.WriteString("Journal recovery\nRetained work facts, not new instructions or fresh workspace validation.\n")
	renderNode := func(item journalItem, full bool) string {
		var node strings.Builder
		fmt.Fprintf(&node, "\n%s", item.Path)
		if item.State != "" {
			fmt.Fprintf(&node, " [%s]", item.State)
		}
		fmt.Fprintf(&node, " %s", item.Title)
		if item.Agent != "" {
			fmt.Fprintf(&node, " (agent %s)", item.Agent)
		}
		if mount := completedAgents[item.Path]; mount != "" && item.Kind == "task" && item.Agent != "" && item.State != "done" && item.State != "dropped" {
			fmt.Fprintf(&node, " · bound agent done; integration remains open (retained result: %s)", mount)
		}
		if item.Turns > 0 {
			fmt.Fprintf(&node, " · %d turns", item.Turns)
		}
		if item.Reason != "" {
			fmt.Fprintf(&node, " · %s", item.Reason)
		}
		if item.Started != nil && item.State == "working" {
			fmt.Fprintf(&node, " · started %s", item.Started.At)
		}
		if item.SupersededBy != "" {
			fmt.Fprintf(&node, " · superseded by %s\n", item.SupersededBy)
			return node.String()
		}
		_, closed := collapsed[item.Path]
		if item.Body != "" && (!closed || full) {
			body := item.Body
			if !full {
				body = summaryExcerpt(body, 1024, "[read the retained node for full detail]")
			}
			fmt.Fprintf(&node, "\n%s\n", body)
		}
		return node.String()
	}
	writeNode := func(item journalItem, full bool) { text.WriteString(renderNode(item, full)) }
	text.WriteString("\nContext:\n")
	for _, item := range items {
		// The synthetic Agents group only anchors unbound agents.
		if item.Kind == "context" && !hiddenBySupersession(item.Path) && !strings.HasSuffix(item.Path, "/@agents") {
			writeNode(item, true)
		}
	}
	next := ""
	text.WriteString("\nOpen tasks:\n")
	for _, state := range []string{"working", "pending", "blocked", ""} {
		for _, item := range items {
			if item.Kind != "task" || item.State != state {
				continue
			}
			if next == "" && !strings.Contains(item.Path, "/@") {
				next = item.Path
			}
			writeNode(item, bounded)
		}
	}
	mandatoryLimit := maxJournalSummaryBytes / 2
	if bounded {
		mandatoryLimit = capacity
	}
	if measure(text.String()) > mandatoryLimit {
		return result, errors.New("journal context and open tasks exceed summary capacity")
	}
	// Reserve the resume target before optional evidence. Keep section boundaries
	// so an omission never leaves a partial node, change range, or diagnostic.
	footer := ""
	if bounded {
		footer = fmt.Sprintf("\nResume: continue %s; consult durable journal reads and retained change/output references.\n", cmp.Or(next, "the journal plan"))
		if measure(text.String())+measure(footer) > capacity {
			return result, errors.New("journal context and open tasks exceed summary capacity")
		}
	}
	var sections []string
	used, evidenceOmitted := measure(text.String())+measure(footer), false
	remaining := func() int {
		if bounded {
			return capacity - used
		}
		return capacity - text.Len()
	}
	writeSection := func(section string, hasEvidence bool) {
		if !bounded {
			text.WriteString(section)
		} else if measure(section) <= remaining() {
			sections = append(sections, section)
			used += measure(section)
		} else if hasEvidence {
			evidenceOmitted = true
		}
	}
	var establishedText strings.Builder
	establishedText.WriteString("\nEstablished results and completed work:\n")
	establishedText.WriteString("Completed task bodies and history are on demand: journal({op:\"read\", p:\"PATH\"}).\n")
	// Keep the newest results, which are nearest current work, in tree order.
	var established []string
	budget, omitted := maxJournalSummaryBytes/2-text.Len()-establishedText.Len()-128, 0
	if bounded {
		budget = remaining()/2 - establishedText.Len() - 128
	}
	isResult := func(item journalItem) bool {
		return item.Kind != "context" && (item.Kind != "task" || item.State == "done" || item.State == "dropped") && !hiddenBySupersession(item.Path)
	}
	for _, item := range slices.Backward(items) {
		if !isResult(item) || collapsedBy(item.Path) != "" {
			continue
		}
		node := renderNode(item, false)
		if omitted > 0 || measure(node) > budget {
			omitted++
			continue
		}
		budget -= measure(node)
		established = append(established, node)
	}
	if omitted > 0 {
		fmt.Fprintf(&establishedText, "\n%d earlier results omitted; journal({op:\"read\", view:\"outline\"}) locates own retained paths.\n", omitted)
	}
	for _, node := range slices.Backward(established) {
		establishedText.WriteString(node)
	}
	writeSection(establishedText.String(), len(established) > 0 || omitted > 0)
	index, err := s.readChangeIndex(j.Workspace)
	if err != nil {
		return result, err
	}
	// Recorded work already reports its outcomes; ranges locate the evidence.
	changes, _ := s.renderChildJournalChanges(ctx, index, j.Thread, 0, false, false)
	changeRecovery := "mchanges --list"
	if bounded {
		changeRecovery = "saved Diff or explicit-ID mchanges reads when utilities are enabled"
	}
	// Ask the shared changes renderer for its empty view rather than duplicating
	// its ownership/retention classification or depending on a prose literal.
	hasChanges := true
	if bounded {
		emptyChanges, _ := s.renderChildJournalChanges(ctx, changeIndex{}, j.Thread, 0, false, false)
		hasChanges = changes != emptyChanges
	}
	writeSection("\nRetained changes:\n"+boundCompactSection(changes, changeRecovery), hasChanges)
	sinceChange, sinceCapture := uint64(0), uint64(0)
	if j.EvidenceKnown {
		sinceChange, sinceCapture = j.EvidenceChangeSeq, j.EvidenceCaptureOrder
	}
	for id, change := range index.Changes {
		streamName, _, _ := parseChangeID(id)
		owner := ""
		for i, stream := range index.Streams {
			if index.streamName(i) == streamName {
				owner = stream.Thread
				break
			}
		}
		if slices.ContainsFunc(change.Calls, func(call trackedCall) bool {
			return cmp.Or(call.Thread, owner) == j.Thread && (!j.EvidenceKnown || call.Sequence > sinceChange)
		}) || !j.EvidenceKnown && len(change.Calls) == 0 && owner == j.Thread {
			result.Changes++
		}
	}
	records, err := s.journalExecutions(j.Workspace, j.Thread)
	if err != nil {
		return result, err
	}
	var failures []replayRecord
	var unconfirmed []string
	completed := make(map[string]bool)
	for _, record := range records {
		if record.History.ExecOutcome != nil {
			completed[record.CallID] = true
		}
	}
	for _, record := range records {
		history := record.History
		outcome := history.ExecOutcome
		if outcome != nil && outcome.Status == execStatusFailed && outcome.SharedWith == "" && (!j.EvidenceKnown || record.CaptureOrder > sinceCapture) {
			failures = append(failures, record)
		}
		if history.ExecObservation != nil && outcome == nil && !completed[execDerivedCallID(record.CallID, history.ExecObservation.CodeMode)] {
			if bounded && len(history.Script) > 512 {
				evidenceOmitted = true
			}
			unconfirmed = append(unconfirmed, fmt.Sprintf("\n%s: %s\n", record.CallID, summaryExcerpt(history.Script, 512, "[source truncated]")))
		}
	}
	if len(unconfirmed) > 0 {
		var observations strings.Builder
		observations.WriteString("\nExecution observations without retained completion:\nPre-execution observations only, not proof of a running process. Check current host state before starting overlapping work. No continuation handle or Code Mode store value is restored.\n")
		start := max(0, len(unconfirmed)-8)
		if start > 0 {
			fmt.Fprintf(&observations, "%d earlier observations omitted.\n", start)
			if bounded {
				evidenceOmitted = true
			}
		}
		for _, entry := range unconfirmed[start:] {
			observations.WriteString(entry)
		}
		writeSection(observations.String(), true)
	}
	result.Failures = len(failures)
	if result.Changes > 0 || len(failures) > 0 {
		var delta strings.Builder
		delta.WriteString("\nSince the last journal event:\n")
		if result.Changes > 0 {
			changes, _ := s.renderChildJournalChanges(ctx, index, j.Thread, sinceChange, true, true)
			delta.WriteString(boundCompactSection(changes, changeRecovery))
		}
		// Keep the newest failures within the remaining capacity.
		var listed []string
		budget := remaining() - measure(delta.String()) - 512
		for _, failure := range slices.Backward(failures) {
			if len(listed) == maxJournalSummaryFailures {
				break
			}
			outcome := failure.History.ExecOutcome
			output, notice := "output not retained", "[earlier output omitted]"
			if outcome.OutputRef != "" {
				name, err := s.outputName(outcome.OutputRef)
				if err != nil {
					return result, err
				}
				data, err := readManagedOutputFile(filepath.Join(s.directory, name))
				if err != nil {
					return result, err
				}
				if _, err := decodeShellOutputRecord(data, outcome.OutputRef); err != nil {
					return result, fmt.Errorf("invalid retained failed-command output: %w", err)
				}
				output, notice = "mread "+outcome.OutputRef, "[earlier output omitted; mread "+outcome.OutputRef+"]"
			}
			exit := "unknown"
			if outcome.Exit != nil {
				exit = fmt.Sprint(*outcome.Exit)
			}
			if bounded && (len(failure.History.Script) > 1024 || outcome.OutputRef == "" && len(failure.History.Report) > 1024) {
				evidenceOmitted = true
			}
			entry := fmt.Sprintf("\nFailed: %s\nExit: %s · %s\n%s\n", summaryExcerpt(failure.History.Script, 1024, "[command truncated]"), exit, output, summaryTail(failure.History.Report, 1024, notice))
			if measure(entry) > budget {
				break
			}
			budget -= measure(entry)
			listed = append(listed, entry)
		}
		if earlier := len(failures) - len(listed); earlier > 0 {
			fmt.Fprintf(&delta, "\n%d earlier failed commands omitted.\n", earlier)
			if bounded {
				evidenceOmitted = true
			}
		}
		for _, entry := range slices.Backward(listed) {
			delta.WriteString(entry)
		}
		writeSection(delta.String(), true)
		if !bounded {
			fmt.Fprintf(&text, "\nResume: continue %s; read the listed journal paths, mchanges ranges and mread references as needed.\n", cmp.Or(next, "the journal plan"))
		}
	} else if !bounded {
		fmt.Fprintf(&text, "\nResume: continue %s; read retained journal paths and mchanges ranges as needed.\n", cmp.Or(next, "the journal plan"))
	}
	if bounded {
		// Omitted command records remain discoverable even when individual mread
		// references cannot fit. The manifest names immutable retained records.
		notice := "\nOptional evidence omitted; durable journal reads and explicit-ID mchanges reviews retain details when utilities are enabled."
		if len(records) > 0 {
			notice += fmt.Sprintf(" Execution records: %q (files relative to %q).", filepath.Join(s.directory, storageSessionName(j.Thread)), s.directory)
		}
		notice += "\n"
		if evidenceOmitted {
			for len(sections) > 0 && used+measure(notice) > capacity {
				used -= measure(sections[len(sections)-1])
				sections = sections[:len(sections)-1]
			}
			if used+measure(notice) > capacity {
				return result, errors.New("journal mandatory facts and evidence recovery references exceed summary capacity")
			}
		}
		for _, section := range sections {
			text.WriteString(section)
		}
		if evidenceOmitted {
			text.WriteString(notice)
		}
		text.WriteString(footer)
	}
	if measure(text.String()) > capacity {
		return result, errors.New("journal evidence exceeds summary capacity")
	}
	result.Text = text.String()
	return result, nil
}
