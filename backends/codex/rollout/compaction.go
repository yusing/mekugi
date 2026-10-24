package rollout

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"strings"
)

// compactionBoundary joins only the next completed native boundary to its
// preceding replacement history. A new turn retires unconfirmed replacements.
func compactionBoundary(rec replayRecord, thread string, pending *string) (string, string) {
	p := rec.Payload
	if rec.Type == "event_msg" && p.Thread != "" && p.Thread != thread {
		return "", ""
	}
	if rec.Type == "session_meta" {
		*pending = ""
	}
	if rec.Type == "compacted" {
		*pending = ""
		count := 0
		for _, item := range p.History {
			if item.Type == "compaction" || item.Type == "compaction_summary" {
				count++
				*pending = item.Reference
			}
		}
		if count != 1 {
			*pending = ""
		}
	}
	if rec.Type != "event_msg" {
		return "", ""
	}
	if p.Type == "task_started" || p.Type == "turn_started" {
		*pending = ""
	}
	if p.Type != "item_completed" {
		return "", ""
	}
	var item struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if json.Unmarshal(p.Item, &item) != nil || !strings.EqualFold(item.Type, "contextCompaction") {
		return "", ""
	}
	reference := *pending
	*pending = ""
	return item.ID, reference
}

// CompactionReferences reads the admitted rollout once for exact boundary IDs.
// References alone confer no retained-session authority.
func CompactionReferences(ctx context.Context, thread, path string) map[string]string {
	result := make(map[string]string)
	var pending string
	first := true
	if err := readReplayLines(ctx, path, func(line []byte) error {
		var rec replayRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if first && (rec.Type != "session_meta" || rec.Payload.ID != thread) {
			return errors.New("compaction rollout identity is unavailable")
		}
		first = false
		id, reference := compactionBoundary(rec, thread, &pending)
		if rec.Payload.Thread == thread && id != "" && reference != "" {
			result[id] = reference
		}
		return nil
	}); err != nil {
		return nil
	}
	return result
}
