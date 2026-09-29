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
	standalone := false
	for _, item := range output {
		if isJournalCall(item) {
			standalone = true
		} else if kind := jsonString(item, "type"); kind != "message" && kind != "reasoning" {
			// Completed hosted work can accompany a final answer, but it is
			// still useful tool work rather than journal-only overhead.
			standalone = false
			break
		}
	}
	if standalone && response.ID != "" {
		t.proxy.journalCounters(t.ctx, t.directory, t.shellThreadID, "counter-request:"+response.ID, func(j *threadJournal) {
			counts := j.journalCounters()
			counts.StandaloneRequests++
			counts.Sequence++
		})
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
	capturable := true
	for _, item := range output {
		if !isSubstantiveAnswer(item) && !(isFinalAnswerMessage(item) && jsonString(item, "phase") == "final_answer" && strings.TrimSpace(commentaryMessageText(item)) == "") {
			continue
		}
		t.journalNaturalFinalSeen = true
		if jsonString(item, "id") == "" || t.finalAnswer.disabled {
			// Streaming may already have exposed an oversized answer. Do not
			// duplicate it under a new journal-owned identity.
			capturable = false
		}
		if answer.Len() != 0 {
			answer.WriteString("\n\n")
		}
		answer.WriteString(commentaryMessageText(item))
		ids = append(ids, jsonString(item, "id"))
	}
	emptyOutcome := strings.TrimSpace(answer.String()) == "" || strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(answer.String()), "."), "done")
	if len(ids) != 0 && response.ID != "" {
		t.proxy.journalCounters(t.ctx, t.directory, t.shellThreadID, "counter-outcome:"+response.ID, func(j *threadJournal) {
			counts := j.journalCounters()
			counts.FinalAnswers++
			counts.FinalAnswerBytes += uint64(answer.Len())
			counts.LastOutcomeEmpty = new(emptyOutcome)
			if emptyOutcome {
				counts.EmptyOutcomes++
			}
			counts.Sequence++
		})
	}
	if !capturable {
		return nil
	}
	if len(ids) != 0 && emptyOutcome {
		tree, err := t.proxy.journals.treeAuthored(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID)
		if err != nil {
			return err
		}
		// Only a turn card stands in for an empty Outcome. A legacy flush has
		// nothing to render, so it keeps the answer as the final message.
		emptyOutcome = tree || t.nativeJournal() != nil
	}
	if len(ids) != 0 && emptyOutcome {
		t.journalNaturalAnswerIDs = make(map[string]bool, len(ids))
		for _, id := range ids {
			t.journalNaturalAnswerIDs[id] = true
		}
		if sink := t.nativeJournal(); sink != nil {
			sink.bindAnswer(ids, "@empty-outcome")
		}
	}
	if strings.TrimSpace(answer.String()) != "" && !emptyOutcome {
		value := answer.String()
		if len(value)+len(t.journalQuestion) > maxJournalItemBytes {
			return nil // The original provider answer remains visible.
		}
		mutation := journalMutation{Op: "add", Text: &value, Answer: new(true), inferredQuestion: t.journalQuestion}
		receipt := response.ID
		if receipt == "" {
			receipt = ids[0]
		}
		journalIDs, err := t.proxy.journals.apply(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, "final:"+receipt, []journalMutation{mutation})
		if err != nil {
			if errors.Is(err, errJournalItemLimit) || errors.Is(err, errJournalEventLimit) {
				return nil // Preserve the original answer and its provider history.
			}
			return err
		}
		// Source: codex-rs/core/src/event_mapping.rs parse_turn_item and
		// app-server-protocol/src/protocol/v2/item.rs@86be5320 copy the provider
		// message ID unchanged into AgentMessage and its delta itemId.
		if sink := t.nativeJournal(); sink != nil && len(journalIDs) > 0 {
			sink.bindAnswer(ids, journalIDs[0])
		}
		t.journalNaturalAnswerIDs = make(map[string]bool, len(ids))
		for _, id := range ids {
			t.journalNaturalAnswerIDs[id] = true
		}
	}
	if len(ids) != 0 {
		t.journalFinishRequested = true
	}
	t.proxy.journalCounters(t.ctx, t.directory, t.shellThreadID, "", nil)
	return nil
}

func (t *mekugiResponseTransform) filterNaturalAnswerEvents(events [][]byte) [][]byte {
	if t.journalNativeSink != nil || len(t.journalNaturalAnswerIDs) == 0 {
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
	if t.journalNativeSink != nil || len(t.journalNaturalAnswerIDs) == 0 {
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
