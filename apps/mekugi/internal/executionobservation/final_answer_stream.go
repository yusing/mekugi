package executionobservation

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Observe answer-only completion for live diff without withholding provider events.
type finalAnswerStream struct {
	substantive bool
	blocked     bool
	disabled    bool
	pending     map[string]int
}

func (s *finalAnswerStream) observe(payload []byte) {
	if s.disabled {
		return
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		s.disabled = true
		return
	}
	var event struct {
		Type        responseevents.Kind          `json:"type"`
		OutputIndex int                          `json:"output_index"`
		Item        map[string]jsonv1.RawMessage `json:"item"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return
	}
	if event.Type == responseevents.Error {
		s.disabled = true
		return
	}
	if !event.Type.ItemEvent() {
		return
	}
	if event.Type == responseevents.OutputItemDone {
		delete(s.pending, responseevents.StringField(event.Item, "id"))
		s.substantive = s.substantive || isSubstantiveAnswer(event.Item)
		s.blocked = s.blocked || blocksTokenUsage(event.Item)
	} else if id := responseevents.StringField(event.Item, "id"); id != "" && isFinalAnswerMessage(event.Item) {
		if s.pending == nil {
			s.pending = make(map[string]int)
		}
		s.pending[id] = event.OutputIndex
	}
}

// A terminal snapshot may finish an answer whose deltas already reached Codex.
// Preserve the provider item and original stream index; never invent an answer.
func (s *finalAnswerStream) complete(response []byte) ([][]byte, error) {
	defer clear(s.pending)
	if s.disabled || len(s.pending) == 0 {
		return nil, nil
	}
	var body struct {
		Status string                         `json:"status"`
		Output []map[string]jsonv1.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(response, &body); err != nil {
		return nil, err
	}
	if body.Status != "" && body.Status != "completed" {
		return nil, nil
	}
	var events [][]byte
	for _, item := range body.Output {
		id := responseevents.StringField(item, "id")
		index, pending := s.pending[id]
		if !pending || !isSubstantiveAnswer(item) {
			continue
		}
		event, err := responseevents.MarshalJSON(map[string]any{"type": responseevents.OutputItemDone, "output_index": index, "item": item})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
		delete(s.pending, id)
	}
	return events, nil
}
