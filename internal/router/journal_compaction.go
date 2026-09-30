package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/commentaryid"
	"github.com/yusing/mekugi/internal/responses"
)

// One latest response per workspace/thread is sufficient: a missing or replaced
// match only repeats hook recovery, never suppresses a provider summary's hook.
type journalCompactionRecord struct {
	Version    int    `json:"version"`
	Workspace  string `json:"workspace"`
	Thread     string `json:"thread"`
	ResponseID string `json:"response_id"`
}

func journalCompactionName(workspace, thread string) string {
	return fmt.Sprintf("compaction-%x.json", sha256.Sum256([]byte(journalKey(workspace, thread))))
}

// Compaction shares ordinary terminal delivery but neither invokes a provider
// nor initializes model usage. Errors before publication fall back upstream.
func (a *requestAttempt) tryJournalCompaction() bool {
	if !a.metadataValid || a.metadata.RequestKind != responses.Compaction {
		return false
	}
	capturer.ObserveCompaction(a.startCtx, "provider", 0, 0, 0)
	p := a.executor.mekugiCalls
	if p == nil || p.replayStore == nil || p.journalCompaction != "auto" && p.journalCompaction != "slice" ||
		a.threadID == "" || a.metadata.activityIdentityInvalid ||
		a.metadata.ThreadID != "" && a.metadata.ThreadID != a.threadID ||
		slices.ContainsFunc(a.headers.Values(threadIDHeader), func(value string) bool {
			return strings.TrimSpace(value) != "" && strings.TrimSpace(value) != a.threadID
		}) ||
		len(a.metadata.Directories) > 1 {
		return false
	}
	workspace, ok := usableRoutingDirectory(a.metadata.Directories)
	if len(a.metadata.Directories) != 0 && !ok {
		return false
	}
	ctx, release, err := p.replayStore.beginSession(a.startCtx, a.threadID, a.sessionID)
	if err != nil {
		return false
	}
	store := p.replayStore.scoped(ctx)
	var summary journalSummary
	var wire []byte
	err = store.locked(ctx, func() error {
		if workspace == "" {
			workspace, err = store.compactionWorkspace(a.threadID)
			if err != nil {
				return err
			}
		}
		j, exists, err := readThreadJournal(store, workspace, a.threadID)
		if err != nil {
			return err
		}
		if !exists {
			// A change-only session has no task state to restore. Durable
			// executing-thread ownership, not the request's prose, supplies it.
			j = threadJournal{Workspace: workspace, Thread: a.threadID, IdentityKnown: true}
		}
		var metadata struct {
			Trigger string `json:"trigger"`
			Phase   string `json:"phase"`
		}
		_ = json.Unmarshal(a.metadata.Compaction, &metadata)
		// Compaction turns do not begin a journal turn, so an intent armed
		// after the completed slice still names the latest ordinary turn.
		reset := j.ResetIntent != nil && j.ResetIntent.Phase == "armed" && j.ResetIntent.Turn == j.TurnID &&
			metadata.Trigger == "manual" && metadata.Phase == "standalone_turn"
		if p.journalCompaction == "slice" && !reset {
			return errJournalUnchanged
		}
		summary, err = store.journalSummaryLocked(ctx, j)
		if err != nil {
			return err
		}
		if !exists && summary.Changes == 0 && summary.Failures == 0 {
			return errors.New("no durable compaction evidence")
		}
		id := "resp_mekugi_compact_" + rand.Text()
		wire, err = journalCompactionSSE(id, a.request.model(), summary.Text)
		if err != nil {
			return err
		}
		record := journalCompactionRecord{Version: 1, Workspace: workspace, Thread: a.threadID, ResponseID: id}
		data, err := json.Marshal(&record)
		if err != nil {
			return err
		}
		if err := store.writeManagedFile(journalCompactionName(workspace, a.threadID), "compaction-pending-", data); err != nil {
			return err
		}
		if reset {
			j.ResetIntent.Phase, j.ResetIntent.ResponseID = "consumed", id
			return writeThreadJournal(store, j)
		}
		return nil
	})
	if err != nil {
		release()
		if !errors.Is(err, errJournalUnchanged) {
			p.notice(a.sessionID, a.threadID, "journal_compaction_fallback", "Journal compaction unavailable; using the provider summary.")
		}
		return false
	}
	a.compactionRelease = release
	a.response = &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(bytes.NewReader(wire)),
	}
	a.streamResponse = true
	a.finalization.upstreamStatusCode = http.StatusOK
	capturer.ObserveCompaction(a.startCtx, "router", len(summary.Text), summary.Changes, summary.Failures)
	return true
}

// Native local compaction omits workspaces from its metadata. In that case only
// the requesting thread's uniquely retained, previously selected workspace can
// supply the directory. Multiple historical workspaces are deliberately ambiguous.
// Called under the replay lock, after acquiring the requesting session lease.
func (s *mekugiReplayStore) compactionWorkspace(thread string) (string, error) {
	session, err := s.readRetainedSession(storageSessionName(thread))
	if err != nil {
		return "", err
	}
	workspace := ""
	for name := range session.Files {
		if !strings.HasPrefix(name, "journal-") && !strings.HasPrefix(name, "call-") {
			continue
		}
		data, err := readManagedOutputFile(filepath.Join(s.directory, name))
		if err != nil {
			return "", err
		}
		var candidate string
		if strings.HasPrefix(name, "journal-") {
			var j threadJournal
			if json.Unmarshal(data, &j) != nil || journalFilename(j.Workspace, j.Thread) != name {
				return "", errors.New("invalid journal workspace evidence")
			}
			if j.Thread == thread {
				if !j.IdentityKnown || j.IdentityConflicted {
					return "", errors.New("conflicted compaction identity")
				}
				candidate = j.Workspace
			}
		} else {
			var record replayRecord
			if json.Unmarshal(data, &record) != nil || (record.Version != 1 && record.Version != 2) || replayRecordName(record.Workspace, record.CallID, record.Commentary) != name {
				return "", errors.New("invalid retained workspace evidence")
			}
			if record.History.ExecutingThread == thread {
				candidate = record.Workspace
			}
		}
		if candidate != "" {
			if workspace != "" && workspace != candidate {
				return "", errors.New("compaction workspace is ambiguous")
			}
			workspace = candidate
		}
	}
	if !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return "", errors.New("no retained compaction workspace")
	}
	return workspace, nil
}

func journalCompactionSSE(id, model, summary string) ([]byte, error) {
	if strings.TrimSpace(summary) == "" {
		return nil, errors.New("empty journal compaction summary")
	}
	item := map[string]any{
		"id": commentaryid.CompactionPrefix + id, "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": summary, "annotations": []any{}}},
	}
	response := map[string]any{
		"id": id, "object": "response", "model": model, "status": "completed", "output": []any{item},
		"usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
	}
	var wire bytes.Buffer
	for i, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "model": model, "status": "in_progress", "output": []any{}}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": response},
	} {
		event["sequence_number"] = i
		data, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		if _, err := writeSSEEvent(&wire, responseSSELines(data, "\n"), "\n", nil, nil); err != nil {
			return nil, err
		}
	}
	return wire.Bytes(), nil
}

// Codex supplies no response ID in SessionStart. Its compacted rollout record
// carries the completed compaction_response_id. Read a bounded tail and fail
// open to ordinary hook recovery when the record is absent or unparseable.
func compactedResponseID(transcript string) string {
	if !filepath.IsAbs(transcript) {
		return ""
	}
	f, err := os.Open(transcript)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	const tailBytes = 4 << 20
	start := max(int64(0), info.Size()-tailBytes)
	data := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(data, start); err != nil {
		return ""
	}
	if start > 0 {
		_, data, _ = bytes.Cut(data, []byte{'\n'})
	}
	var responseID string
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				ResponseID string `json:"compaction_response_id"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil {
			return ""
		}
		if row.Type == "compacted" {
			responseID = row.Payload.ResponseID
		}
	}
	return responseID
}

func (s *mekugiReplayStore) answeredCompaction(ctx context.Context, workspace, thread, responseID string) bool {
	if responseID == "" {
		return false
	}
	matched := false
	err := s.locked(ctx, func() error {
		data, err := readManagedOutputFile(filepath.Join(s.directory, journalCompactionName(workspace, thread)))
		if err != nil {
			return err
		}
		var record journalCompactionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		matched = record.Version == 1 && record.Workspace == workspace && record.Thread == thread && record.ResponseID == responseID
		return nil
	})
	return err == nil && matched
}
