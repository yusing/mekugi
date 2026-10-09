package router

import (
	json "encoding/json/v2"
	"path/filepath"
	"slices"
)

// The host-selected rollout, not buffered UI state, pairs a completed item with
// the preceding installed response. Legacy rollouts lack this item evidence.
func rolloutCompactionResponse(info appServerThreadInfo, turn, item string) string {
	matched := false
	for line := range reverseThreadRolloutRecords(info) {
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				Type     string `json:"type"`
				Thread   string `json:"thread_id"`
				Turn     string `json:"turn_id"`
				Response string `json:"compaction_response_id"`
				Item     struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil {
			return ""
		}
		p := row.Payload
		if row.Type == "event_msg" && p.Type == "item_completed" && p.Item.Type == "ContextCompaction" {
			if matched {
				return "" // Another item cannot share this installed response.
			}
			matched = p.Thread == info.ID && p.Turn == turn && p.Item.ID == item
		} else if matched {
			if row.Type == "compacted" {
				return p.Response
			}
			if row.Type == "event_msg" && (p.Type == "task_started" || p.Type == "turn_started" || p.Type == "task_complete" || p.Type == "turn_complete") {
				return ""
			}
		}
	}
	return ""
}

func (u *appServerUI) bindRolloutCompaction(thread, turn, item string) {
	info := u.session.compactionRollout
	if thread != u.thread || info.ID != thread || turn == "" || item == "" || u.proxy == nil || u.proxy.replayStore == nil {
		return
	}
	response := rolloutCompactionResponse(info, turn, item)
	if response == "" {
		return
	}
	s := u.proxy.replayStore
	_ = s.locked(u.ctx, func() error {
		data, err := readManagedOutputFile(filepath.Join(s.directory, journalCompactionRecoveryName(info.Cwd, thread, response)))
		if err != nil {
			return err
		}
		var recovery journalCompactionRecovery
		if err := json.Unmarshal(data, &recovery); err != nil {
			return err
		}
		if recovery.Workspace != info.Cwd || recovery.Thread != thread || recovery.Turn != turn || recovery.ResponseID != response {
			return nil
		}
		data, err = readManagedOutputFile(filepath.Join(s.directory, journalCompactionName(info.Cwd, thread)))
		if err != nil {
			return err
		}
		var record journalCompactionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.Version != 1 || record.Workspace != info.Cwd || record.Thread != thread || slices.ContainsFunc(record.AnsweredItems, func(entry journalCompactionItem) bool { return entry.Turn == turn && entry.Item == item }) {
			return nil
		}
		record.AnsweredItems = append(record.AnsweredItems, journalCompactionItem{Turn: turn, Item: item, ResponseID: response})
		data, err = json.Marshal(&record)
		if err != nil {
			return err
		}
		return s.writeManagedFile(journalCompactionName(info.Cwd, thread), "compaction-pending-", data)
	})
}
