package router

import (
	"bytes"
	"encoding/json"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

// Observe completion evidence without withholding provider answer events.
// Message lifecycle IDs keep terminal journal decoration from replaying or
// replacing an answer whose lifecycle has already reached the host.
type finalAnswerStream struct {
	itemIDs     map[string]bool
	doneIDs     map[string]bool
	substantive bool
	blocked     bool
	disabled    bool
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
		Type responseevents.Kind        `json:"type"`
		Item map[string]json.RawMessage `json:"item"`
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
		s.substantive = s.substantive || isSubstantiveAnswer(event.Item)
		s.blocked = s.blocked || blocksTokenUsage(event.Item)
	}
	if jsonString(event.Item, "type") == "message" {
		if id := jsonString(event.Item, "id"); id != "" {
			if s.doneIDs == nil {
				s.itemIDs = make(map[string]bool)
				s.doneIDs = make(map[string]bool)
			}
			s.itemIDs[id] = true
			if event.Type == responseevents.OutputItemDone {
				s.doneIDs[id] = true
			}
		}
	}
}
