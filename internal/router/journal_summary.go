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

// journalFailures reads immutable execution evidence owned by the selected
// session. It never searches model prose for status, nor reads the workspace.
// Called under store.lock; a failed evidence read cannot mean no failures.
// With a known boundary, unordered records predate failure capture ordering and
// are therefore covered by it.
func (s *mekugiReplayStore) journalFailures(workspace, thread string, since uint64, bounded bool) ([]replayRecord, error) {
	session, err := s.readRetainedSession(storageSessionName(thread))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var failures []replayRecord
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
		outcome := record.History.ExecOutcome
		if outcome == nil || outcome.Status != execStatusFailed || outcome.SharedWith != "" || bounded && record.CaptureOrder <= since {
			continue
		}
		failures = append(failures, record)
	}
	slices.SortFunc(failures, func(a, b replayRecord) int {
		return cmp.Or(cmp.Compare(a.CaptureOrder, b.CaptureOrder), strings.Compare(a.CallID, b.CallID))
	})
	return failures, nil
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
		if item.Turns > 0 {
			fmt.Fprintf(&node, " · %d turns", item.Turns)
		}
		if item.Reason != "" {
			fmt.Fprintf(&node, " · %s", item.Reason)
		}
		if item.Started != nil && item.State == "working" {
			fmt.Fprintf(&node, " · started %s", item.Started.At)
		}
		if item.Body != "" {
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
		if item.Kind == "context" {
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
			writeNode(item, false)
		}
	}
	if text.Len() > maxJournalSummaryBytes/2 {
		return result, errors.New("journal context and open tasks exceed summary capacity")
	}
	text.WriteString("\nEstablished results and completed work:\n")
	// Keep the newest results, which are nearest current work, in tree order.
	var established []string
	budget, omitted := maxJournalSummaryBytes/2-text.Len()-128, 0
	for _, item := range slices.Backward(items) {
		if item.Kind == "context" || item.Kind == "task" && item.State != "done" && item.State != "dropped" {
			continue
		}
		node := renderNode(item, false)
		if omitted > 0 || len(node) > budget {
			omitted++
			continue
		}
		budget -= len(node)
		established = append(established, node)
	}
	if omitted > 0 {
		fmt.Fprintf(&text, "\n%d earlier results omitted; journal({op:\"read\"}) retains the complete tree.\n", omitted)
	}
	for _, node := range slices.Backward(established) {
		text.WriteString(node)
	}
	index, err := s.readChangeIndex(j.Workspace)
	if err != nil {
		return result, err
	}
	text.WriteString("\nRetained changes:\n")
	changes, _ := s.renderChildJournalChanges(ctx, index, j.Thread, 0, false)
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
	failures, err := s.journalFailures(j.Workspace, j.Thread, sinceCapture, j.EvidenceKnown)
	if err != nil {
		return result, err
	}
	result.Failures = len(failures)
	if result.Changes > 0 || len(failures) > 0 {
		text.WriteString("\nSince the last journal event:\n")
		if result.Changes > 0 {
			changes, _ := s.renderChildJournalChanges(ctx, index, j.Thread, sinceChange, true)
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
		fmt.Fprintf(&text, "\nResume: read the listed mchanges ranges and mread references, then continue %s.\n", cmp.Or(next, "the journal plan"))
	} else {
		fmt.Fprintf(&text, "\nResume: continue %s; use journal({op:\"read\"}) and mchanges for retained details.\n", cmp.Or(next, "the journal plan"))
	}
	if text.Len() > maxJournalSummaryBytes {
		return result, errors.New("journal evidence exceeds summary capacity")
	}
	result.Text = text.String()
	return result, nil
}
