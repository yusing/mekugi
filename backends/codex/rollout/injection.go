package rollout

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"slices"
)

type injectedMessage struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Role    string `json:"role"`
	Phase   string `json:"phase"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Metadata struct {
		Kinds []string `json:"content_item_kinds"`
	} `json:"internal_chat_message_metadata_passthrough"`
}

// ConfirmInjection establishes only positive evidence from an admitted rollout.
// Missing, compacted-away or truncated evidence cannot authorize another effect.
func ConfirmInjection(ctx context.Context, thread, path string, expected jsontext.Value) error {
	var wanted injectedMessage
	if json.Unmarshal(expected, &wanted) != nil || wanted.Type != "message" || wanted.ID == "" || wanted.Role != "assistant" || len(wanted.Content) == 0 || len(wanted.Metadata.Kinds) != len(wanted.Content) {
		return errors.New("injection reconciliation requires complete attributed message identity")
	}
	found := false
	match := func(raw jsontext.Value) error {
		var actual injectedMessage
		if err := json.Unmarshal(raw, &actual); err != nil {
			return err
		}
		if actual.ID != wanted.ID {
			return nil
		}
		if actual.Type != wanted.Type || actual.Role != wanted.Role || actual.Phase != wanted.Phase || !slices.Equal(actual.Content, wanted.Content) || !slices.Equal(actual.Metadata.Kinds, wanted.Metadata.Kinds) {
			return errors.New("retained injection identity has conflicting content or provenance")
		}
		found = true
		return nil
	}
	for line := range reverseRecords(thread, path) {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record struct {
			Type    string         `json:"type"`
			Payload jsontext.Value `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		switch record.Type {
		case "response_item":
			if err := match(record.Payload); err != nil {
				return err
			}
		case "compacted":
			var compacted struct {
				History []jsontext.Value `json:"replacement_history"`
			}
			if err := json.Unmarshal(record.Payload, &compacted); err != nil {
				return err
			}
			for _, item := range compacted.History {
				if err := match(item); err != nil {
					return err
				}
			}
		}
	}
	if !found {
		return errors.New("injection remains uncertain: exact item is not verified in bounded admitted history")
	}
	return nil
}
