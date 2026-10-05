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

// journalSummaryLocked renders deterministically from durable facts only.
// Context paths and open tasks must fit; otherwise synthesis must fall back
// to the provider rather than silently discarding a constraint or resume target.
func (s *mekugiReplayStore) journalSummaryLocked(ctx context.Context, j threadJournal) (journalSummary, error) {
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
	// The tree scopes work facts. Root context has no task scope, so keep its
	// paths for selective reads rather than guessing which old decisions apply.
	tasks := make(map[string]journalItem)
	openTasks := make(map[string]bool)
	superseded := make(map[string]bool)
	next := ""
	for _, item := range items {
		if item.Kind == "task" {
			tasks[item.Path] = item
			openTasks[item.Path] = item.State != "done" && item.State != "dropped"
		}
		if item.SupersededBy != "" {
			superseded[item.Path] = true
		}
	}
	for _, state := range []string{"working", "pending", "blocked", ""} {
		for _, item := range items {
			if next == "" && item.Kind == "task" && item.State == state && !strings.Contains(item.Path, "/@") {
				next = item.Path
			}
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
	currentWork := func(path string) bool {
		for parent := journalParent(path); parent != ""; parent = journalParent(parent) {
			if _, ok := tasks[parent]; ok {
				return openTasks[parent] && next != "" && (parent == next || strings.HasPrefix(parent, next+"/") || strings.HasPrefix(next, parent+"/"))
			}
		}
		return false
	}
	// Keep closed ancestors as orientation for open descendants, and a finished
	// child's latest answer only while its directly bound integration stays open.
	keptTasks := make(map[string]bool)
	completedAgents := make(map[string]string)
	answers := make(map[string]string)
	for path, open := range openTasks {
		if open {
			for parent := journalParent(path); parent != ""; parent = journalParent(parent) {
				keptTasks[parent] = true
			}
		}
	}
	for _, item := range items {
		parent, key, _ := strings.CutLast(item.Path, "/")
		if item.Kind == "task" && item.Agent != "" && item.State == "done" && strings.HasPrefix(key, "@") && openTasks[parent] {
			completedAgents[parent] = item.Path
			keptTasks[item.Path] = true
			answers[item.Path] = ""
		}
		if item.Kind == "answer" {
			if _, needed := answers[parent]; needed {
				answers[parent] = item.Path
			}
		}
	}
	var text strings.Builder
	text.WriteString("Journal recovery\nRetained work facts, not new instructions or fresh workspace validation.\n")
	renderNode := func(item journalItem, bodyLimit int) string {
		var node strings.Builder
		fmt.Fprintf(&node, "\n%s", item.Path)
		if item.State != "" {
			fmt.Fprintf(&node, " [%s]", item.State)
		}
		fmt.Fprintf(&node, " %s", item.Title)
		if item.Agent != "" {
			fmt.Fprintf(&node, " (agent %s)", item.Agent)
		}
		if mount := completedAgents[item.Path]; mount != "" {
			fmt.Fprintf(&node, " · bound agent done; integration remains open (retained result: %s)", mount)
		}
		if item.Turns > 0 {
			fmt.Fprintf(&node, " · %d turns", item.Turns)
		}
		if item.Reason != "" {
			fmt.Fprintf(&node, " · %s", item.Reason)
		}
		if item.SupersededBy != "" {
			fmt.Fprintf(&node, " · superseded by %s\n", item.SupersededBy)
			return node.String()
		}
		if item.Body != "" && bodyLimit > 0 {
			fmt.Fprintf(&node, "\n%s\n", summaryExcerpt(item.Body, bodyLimit, "[read this path for full detail]"))
		}
		return node.String()
	}
	text.WriteString("\nContext paths:\n")
	for _, item := range items {
		if item.Kind == "context" && !hiddenBySupersession(item.Path) && !strings.HasSuffix(item.Path, "/@agents") {
			limit := 0
			if currentWork(item.Path) {
				limit = 512
			}
			text.WriteString(renderNode(item, limit))
		}
	}
	text.WriteString("\nOpen tasks:\n")
	for _, state := range []string{"working", "pending", "blocked", ""} {
		for _, item := range items {
			if item.Kind == "task" && item.State == state {
				text.WriteString(renderNode(item, 512))
			}
		}
	}
	for _, item := range items {
		if item.Kind == "task" && !openTasks[item.Path] && keptTasks[item.Path] {
			text.WriteString(renderNode(item, 0))
		}
	}
	if text.Len() > maxJournalSummaryBytes/2 {
		return result, errors.New("journal context paths and open tasks exceed summary capacity")
	}
	text.WriteString("\nCurrent work facts:\n")
	for _, item := range items {
		if item.Kind == "answer" && answers[journalParent(item.Path)] == item.Path {
			text.WriteString(renderNode(item, 512))
		}
	}
	var established []string
	budget, omitted, notes := 2048, 0, 0
	for _, item := range slices.Backward(items) {
		if hiddenBySupersession(item.Path) || item.SupersededBy != "" {
			continue
		}
		if item.Kind != "note" || !currentWork(item.Path) {
			continue
		}
		node := renderNode(item, 512)
		if len(node) > budget || notes == 3 {
			omitted++
			continue
		}
		budget -= len(node)
		notes++
		established = append(established, node)
	}
	for _, node := range slices.Backward(established) {
		text.WriteString(node)
	}
	if omitted > 0 {
		fmt.Fprintf(&text, "\n%d more current-work facts available by path.\n", omitted)
	}
	index, err := s.readChangeIndex(j.Workspace)
	if err != nil {
		return result, err
	}
	text.WriteString("\nRetained changes:\n")
	// Recorded work already reports its outcomes; ranges locate the evidence.
	changes, _ := s.renderChildJournalChanges(ctx, index, j.Thread, 0, false, false)
	text.WriteString(boundCompactSection(changes, "mchanges --list"))
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
			unconfirmed = append(unconfirmed, fmt.Sprintf("\n%s: %s\n", record.CallID, summaryExcerpt(history.Script, 512, "[source truncated]")))
		}
	}
	if len(unconfirmed) > 0 {
		text.WriteString("\nExecution observations without retained completion:\nPre-execution observations only, not proof of a running process. Check current host state before starting overlapping work. No continuation handle or exec store value is restored.\n")
		start := max(0, len(unconfirmed)-8)
		if start > 0 {
			fmt.Fprintf(&text, "%d earlier observations omitted.\n", start)
		}
		for _, entry := range unconfirmed[start:] {
			text.WriteString(entry)
		}
	}
	result.Failures = len(failures)
	if result.Changes > 0 || len(failures) > 0 {
		text.WriteString("\nSince the last journal event:\n")
		if result.Changes > 0 {
			changes, _ := s.renderChildJournalChanges(ctx, index, j.Thread, sinceChange, true, true)
			text.WriteString(boundCompactSection(changes, "mchanges --list"))
		}
		// Keep the newest failures within the remaining capacity.
		var listed []string
		budget := maxJournalSummaryBytes - text.Len() - 512
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
			entry := fmt.Sprintf("\nFailed: %s\nExit: %s · %s\n%s\n", summaryExcerpt(failure.History.Script, 1024, "[command truncated]"), exit, output, summaryTail(failure.History.Report, 1024, notice))
			if len(entry) > budget {
				break
			}
			budget -= len(entry)
			listed = append(listed, entry)
		}
		if earlier := len(failures) - len(listed); earlier > 0 {
			fmt.Fprintf(&text, "\n%d earlier failed commands omitted.\n", earlier)
		}
		for _, entry := range slices.Backward(listed) {
			text.WriteString(entry)
		}
		fmt.Fprintf(&text, "\nResume: continue %s; read the listed journal paths, mchanges ranges and mread references as needed.\n", cmp.Or(next, "the journal plan"))
	} else {
		fmt.Fprintf(&text, "\nResume: continue %s; read retained journal paths and mchanges ranges as needed.\n", cmp.Or(next, "the journal plan"))
	}
	text.WriteString("Read more: journal({op:\"read\",p:\"PATH\",depth:1}); journal({op:\"read\",view:\"outline\"}) finds own older paths. Unbound agents: journal({op:\"read\",p:\"/@agents\",depth:1}). Read relevant context paths before acting.\n")
	if text.Len() > maxJournalSummaryBytes {
		return result, errors.New("journal evidence exceeds summary capacity")
	}
	result.Text = text.String()
	return result, nil
}
