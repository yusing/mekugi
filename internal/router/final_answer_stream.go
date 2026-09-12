package router

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Hold provider answer events until token-usage ordering is known. Every buffered
// provider event is released unchanged, including on failure or buffer exhaustion.
type finalAnswerStream struct {
	events      [][]byte
	itemIDs     map[string]bool
	indexes     map[int]bool
	bytes       int
	substantive bool
	blocked     bool
	disabled    bool
}

func (s *finalAnswerStream) observe(payload []byte) ([][]byte, bool) {
	var event struct {
		Type        string                     `json:"type"`
		ItemID      string                     `json:"item_id"`
		OutputIndex *int                       `json:"output_index"`
		Item        map[string]json.RawMessage `json:"item"`
	}
	if s.disabled {
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		s.disabled = true
		return append(s.flush(), payload), true
	}
	if json.Unmarshal(payload, &event) != nil {
		return nil, false
	}
	if event.Type == "error" {
		s.disabled = true
		return append(s.flush(), payload), true
	}
	itemEvent := event.Type == "response.output_item.added" || event.Type == "response.output_item.done"
	if event.Type == "response.output_item.done" {
		s.substantive = s.substantive || isSubstantiveAnswer(event.Item)
		s.blocked = s.blocked || blocksTokenUsage(event.Item)
	}
	answer := false
	if itemEvent {
		answer = isFinalAnswerMessage(event.Item)
		if answer {
			if s.itemIDs == nil {
				s.itemIDs = make(map[string]bool)
				s.indexes = make(map[int]bool)
			}
			if id := jsonString(event.Item, "id"); id != "" {
				s.itemIDs[id] = true
			}
			if event.OutputIndex != nil {
				s.indexes[*event.OutputIndex] = true
			}
		}
	} else if strings.HasPrefix(event.Type, "response.") {
		if event.ItemID != "" {
			answer = s.itemIDs[event.ItemID]
		} else if event.OutputIndex != nil {
			answer = s.indexes[*event.OutputIndex]
		}
	}
	if !answer {
		return nil, false
	}
	// Auxiliary usage must not reject large answers or retain unbounded streams.
	if len(payload) > upstreamJSONBufferBytes-s.bytes {
		s.disabled = true
		return append(s.flush(), payload), true
	}
	s.events = append(s.events, bytes.Clone(payload))
	s.bytes += len(payload)
	return nil, true
}

func (s *finalAnswerStream) flush() [][]byte {
	events := s.events
	s.events = nil
	s.itemIDs = nil
	s.indexes = nil
	s.bytes = 0
	return events
}

// FlushSSE releases provider events unchanged on EOF or transport/transform
// failure. Usage requires a successful terminal and is never synthesized here.
func (t *mekugiResponseTransform) FlushSSE() ([][]byte, error) {
	t.finalAnswer.disabled = true
	return t.finalAnswer.flush(), nil
}
