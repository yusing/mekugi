package router

import (
	"encoding/json"
	"errors"
	"strings"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

// A completed provider answer is the terminal journal answer. Capture it before
// terminal delivery so only the journal renderer owns its visible copy.
func (t *mekugiResponseTransform) captureNaturalJournalAnswer(payload []byte) error {
	if !t.journalAvailable || t.journalClientCalls || len(t.journalPending) != 0 {
		return nil
	}
	var response struct {
		ID     string                       `json:"id"`
		Status string                       `json:"status"`
		Output []map[string]json.RawMessage `json:"output"`
	}
	if json.Unmarshal(payload, &response) != nil || response.Status != "completed" {
		return nil
	}
	output := response.Output
	if len(output) == 0 {
		output = t.journalProviderOutput
	}
	for _, item := range output {
		if isRouterLocalCall(item) {
			if len(t.journalResults) == 0 {
				return nil
			}
			continue
		}
		if blocksTokenUsage(item) {
			return nil
		}
	}
	for _, result := range t.journalResults {
		var outcome struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal([]byte(jsonString(result, "output")), &outcome) != nil || !outcome.OK {
			return nil
		}
	}
	var answer strings.Builder
	var ids []string
	for _, item := range output {
		if !isSubstantiveAnswer(item) {
			continue
		}
		t.journalNaturalFinalSeen = true
		if jsonString(item, "id") == "" || t.finalAnswer.disabled {
			// Streaming may already have exposed an oversized answer. Do not
			// duplicate it under a new journal-owned identity.
			answer.Reset()
			ids = nil
			break
		}
		if answer.Len() != 0 {
			answer.WriteString("\n\n")
		}
		answer.WriteString(commentaryMessageText(item))
		ids = append(ids, jsonString(item, "id"))
	}
	if answer.Len() != 0 {
		value := answer.String()
		if len(value)+len(t.journalQuestion) > maxJournalItemBytes {
			return nil // The original provider answer remains visible.
		}
		mutation := journalMutation{Op: "add", Text: &value, Answer: new(true), inferredQuestion: t.journalQuestion}
		receipt := response.ID
		if receipt == "" {
			receipt = ids[0]
		}
		if _, err := t.proxy.journals.apply(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, "final:"+receipt, []journalMutation{mutation}); err != nil {
			if errors.Is(err, errJournalItemLimit) {
				return nil // Preserve the original answer and its provider history.
			}
			return err
		}
		t.journalNaturalAnswerIDs = make(map[string]bool, len(ids))
		for _, id := range ids {
			t.journalNaturalAnswerIDs[id] = true
		}
	}
	if len(ids) != 0 {
		t.journalFinishRequested = true
	}
	return nil
}

func (t *mekugiResponseTransform) filterNaturalAnswerEvents(events [][]byte) [][]byte {
	if len(t.journalNaturalAnswerIDs) == 0 {
		return events
	}
	type event struct {
		Type        responseevents.Kind        `json:"type"`
		ItemID      string                     `json:"item_id"`
		OutputIndex *int                       `json:"output_index"`
		Item        map[string]json.RawMessage `json:"item"`
	}
	indexes := make(map[int]bool)
	for _, payload := range events {
		var current event
		if json.Unmarshal(payload, &current) == nil && current.OutputIndex != nil && t.journalNaturalAnswerIDs[jsonString(current.Item, "id")] {
			indexes[*current.OutputIndex] = true
		}
	}
	visible := make([][]byte, 0, len(events))
	for _, payload := range events {
		var current event
		if json.Unmarshal(payload, &current) == nil {
			if t.journalNaturalAnswerIDs[current.ItemID] || t.journalNaturalAnswerIDs[jsonString(current.Item, "id")] ||
				current.OutputIndex != nil && indexes[*current.OutputIndex] {
				continue
			}
		}
		visible = append(visible, payload)
	}
	return visible
}

func (t *mekugiResponseTransform) withoutNaturalAnswer(output []map[string]json.RawMessage) []map[string]json.RawMessage {
	if len(t.journalNaturalAnswerIDs) == 0 {
		return output
	}
	kept := make([]map[string]json.RawMessage, 0, len(output))
	for _, item := range output {
		if !t.journalNaturalAnswerIDs[jsonString(item, "id")] {
			kept = append(kept, item)
		}
	}
	return kept
}
