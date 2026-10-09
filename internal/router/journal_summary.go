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

	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

const (
	maxJournalSummaryBytes    = 64 << 10
	maxJournalSummaryFailures = 8
	// Fuller bodies for the resume branch and root scope share one budget, so
	// the next step's inputs survive reset without a recovery read.
	journalSummaryFullBodies = 12 << 10
	journalSummaryFullBody   = 2 << 10
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
	return text[:limit] + "... " + notice
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
	return notice + " ..." + text[start:]
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
	// The tree scopes work facts. Root context has no task scope and applies to
	// all work, so its newest bodies share the full-body budget.
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
	resume, blocker := j.continuationCandidate(""), j.continuationBlocker()
	if resume != nil {
		next = resume.Path
	} else if blocker != nil {
		next = blocker.Path
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
	// Recovery uses existing read-agent selectors and child-local paths. Mount
	// addresses remain internal to the combined view and its selection logic.
	mounts := make(map[string]journalItem)
	for _, item := range items {
		_, key, _ := strings.CutLast(item.Path, "/")
		if item.Kind == "task" && item.Agent != "" && strings.HasPrefix(key, "@") {
			mounts[item.Path] = item
		}
	}
	mountPath := func(path string) string {
		if before, mounted, ok := strings.CutLast(path, "/@"); ok {
			key, _, _ := strings.Cut(mounted, "/")
			return before + "/@" + key
		}
		return ""
	}
	var text strings.Builder
	text.WriteString("Journal recovery\nRetained work facts, not new instructions or fresh workspace validation.\n")
	workspaceText := ""
	if j.Workspace != "" {
		workspaceText = fmt.Sprintf("\nWorkspace: %q\n", j.Workspace)
	}
	renderNode := func(item journalItem, bodyLimit int) string {
		var node strings.Builder
		fmt.Fprintf(&node, "\n%s", journalLocalPath(item.Path))
		if item.State != "" {
			fmt.Fprintf(&node, " [%s]", item.State)
		}
		fmt.Fprintf(&node, " %s", item.Title)
		if item.Agent != "" {
			node.WriteString(" (delegated)")
		}
		if completedAgents[item.Path] != "" {
			node.WriteString("; child done; integration remains open")
		}
		if item.Reason != "" {
			fmt.Fprintf(&node, "; %s", item.Reason)
		}
		if item.SupersededBy != "" {
			fmt.Fprintf(&node, "; superseded by %s\n", journalLocalPath(item.SupersededBy))
			return node.String()
		}
		if item.Body != "" && bodyLimit > 0 {
			fmt.Fprintf(&node, "\n%s\n", summaryExcerpt(item.Body, bodyLimit, "[read this path for full detail]"))
		}
		return node.String()
	}
	rootContext := func(item journalItem) bool {
		if item.Kind != "context" || strings.Contains(item.Path, "/@") || hiddenBySupersession(item.Path) || item.SupersededBy != "" {
			return false
		}
		for parent := journalParent(item.Path); parent != ""; parent = journalParent(parent) {
			if _, ok := tasks[parent]; ok {
				return false
			}
		}
		return true
	}
	bodyLimit := func(item journalItem) int {
		if item.Kind == "task" {
			if openTasks[item.Path] {
				return 512
			}
			return 0
		}
		if currentWork(item.Path) {
			return 512
		}
		return 0
	}
	// Indexed paths and open tasks are mandatory; fuller bodies only use what
	// the reserved half leaves, so they never cause a capacity failure.
	reserved := 1 << 10
	for _, item := range items {
		if item.Kind == "context" && !hiddenBySupersession(item.Path) && !strings.HasSuffix(item.Path, "/@agents") || item.Kind == "task" && (openTasks[item.Path] || keptTasks[item.Path]) {
			reserved += len(renderNode(item, bodyLimit(item)))
		}
	}
	var candidates []journalItem
	for path := next; path != ""; path = journalParent(path) {
		if task, ok := tasks[path]; ok && openTasks[path] {
			candidates = append(candidates, task)
		}
	}
	for _, item := range slices.Backward(items) {
		if rootContext(item) {
			candidates = append(candidates, item)
		}
	}
	fullBody := make(map[string]int)
	fullBytes, budget := 0, min(journalSummaryFullBodies, maxJournalSummaryBytes/2-reserved)
	contextOmitted := false
	for _, item := range candidates {
		cost := len(renderNode(item, journalSummaryFullBody)) - len(renderNode(item, bodyLimit(item)))
		if cost == 0 {
			continue
		}
		if cost > budget {
			contextOmitted = contextOmitted || item.Kind == "context"
			continue
		}
		budget -= cost
		fullBytes += cost
		fullBody[item.Path] = journalSummaryFullBody
	}
	agentText := make(map[string]*strings.Builder)
	lastSection := make(map[string]string)
	var agentOrder []string
	writeNode := func(section string, item journalItem, bodyLimit int) {
		owner := mountPath(item.Path)
		out := &text
		if owner != "" {
			mount := mounts[owner]
			out = agentText[owner]
			if out == nil {
				out = new(strings.Builder)
				agentText[owner] = out
				agentOrder = append(agentOrder, owner)
				fmt.Fprintf(out, "\nAgent %s", activityui.AgentDisplayName(mount.Agent))
				if mount.State != "" {
					fmt.Fprintf(out, " [%s]", mount.State)
				}
				if mount.Turns > 0 {
					fmt.Fprintf(out, "; %d turns", mount.Turns)
				}
				if mount.Reason != "" {
					fmt.Fprintf(out, "; %s", mount.Reason)
				}
				parent := journalParent(owner)
				if !strings.HasSuffix(parent, "/@agents") {
					fmt.Fprintf(out, "; parent task %s", journalLocalPath(parent))
				}
				if strings.HasSuffix(owner, "/@pending") {
					out.WriteString("; unresolved binding")
				}
				out.WriteByte('\n')
			}
			if item.Path == owner {
				return // The group heading already carries the mount lifecycle.
			}
		}
		if lastSection[owner] != section {
			fmt.Fprintf(out, "\n%s:\n", section)
			lastSection[owner] = section
		}
		out.WriteString(renderNode(item, bodyLimit))
	}
	for _, item := range items {
		if item.Kind == "context" && !hiddenBySupersession(item.Path) && !strings.HasSuffix(item.Path, "/@agents") {
			writeNode("Context paths", item, cmp.Or(fullBody[item.Path], bodyLimit(item)))
		}
	}
	for _, state := range []string{"working", "pending", "blocked", ""} {
		for _, item := range items {
			if item.Kind == "task" && item.State == state {
				writeNode("Open tasks", item, cmp.Or(fullBody[item.Path], 512))
			}
		}
	}
	for _, item := range items {
		if item.Kind == "task" && !openTasks[item.Path] && keptTasks[item.Path] {
			writeNode("Open tasks", item, 0)
		}
	}
	mandatoryBytes := text.Len() - fullBytes
	for _, out := range agentText {
		mandatoryBytes += out.Len()
	}
	if mandatoryBytes > maxJournalSummaryBytes/2 {
		return result, errors.New("journal context paths and open tasks exceed summary capacity")
	}
	for _, item := range items {
		if item.Kind == "answer" && answers[journalParent(item.Path)] == item.Path {
			writeNode("Current work facts", item, 512)
		}
	}
	var established []journalItem
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
		established = append(established, item)
	}
	for _, item := range slices.Backward(established) {
		writeNode("Current work facts", item, 512)
	}
	if omitted > 0 {
		fmt.Fprintf(&text, "\n%d more current-work facts available by path.\n", omitted)
	}
	for _, owner := range agentOrder {
		text.WriteString(agentText[owner].String())
	}
	index, err := s.readChangeIndex(j.Workspace)
	if err != nil {
		return result, err
	}
	text.WriteString("\nRetained changes:\n")
	// Recorded work already reports its outcomes; ranges locate the evidence.
	changes, _ := s.renderChildJournalChanges(ctx, index, j.Thread, 0, false, false)
	text.WriteString(boundCompactSection(strings.TrimSpace(strings.TrimPrefix(changes, "\n\n**Changes:**"))+"\n", "mchanges --list"))
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
			unconfirmed = append(unconfirmed, fmt.Sprintf("\nObserved: %s\n", summaryExcerpt(history.Script, 512, "[source truncated]")))
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
			text.WriteString(boundCompactSection(strings.TrimSpace(strings.Replace(changes, "**Changes:**", "Changes:", 1))+"\n", "mchanges --list"))
		}
		// Keep the newest failures within the remaining capacity.
		var listed []string
		budget := maxJournalSummaryBytes - text.Len() - len(workspaceText) - 512
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
			entry := fmt.Sprintf("\nFailed: %s\nExit: %s; %s\n%s\n", summaryExcerpt(failure.History.Script, 1024, "[command truncated]"), exit, output, summaryTail(failure.History.Report, 1024, notice))
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
	}
	switch {
	case blocker != nil:
		fmt.Fprintf(&text, "\nContinuation paused: %s: %s\n", blocker.Path, blocker.Reason)
	case resume != nil:
		fmt.Fprintf(&text, "\nResume: continue %s; read listed journal paths, mchanges ranges and mread references as needed.\n", resume.Path)
	default:
		text.WriteString("\nResume: no runnable local task.\n")
	}
	text.WriteString("Read more: tools.mcp__mekugi__journal_read({p:\"PATH\",depth:1}); use view:\"outline\" for older paths. For an agent, add agent:\"NAME\",view:\"own\" using its heading. Find older agents with tools.mcp__mekugi__journal_read({depth:1}).\n")
	if contextOmitted {
		text.WriteString("Root context listed without its body has detail by path.\n")
	}
	text.WriteString(workspaceText)
	if text.Len() > maxJournalSummaryBytes {
		return result, errors.New("journal evidence exceeds summary capacity")
	}
	result.Text = text.String()
	return result, nil
}
