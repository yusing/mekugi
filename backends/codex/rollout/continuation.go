package rollout

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
)

// ConfirmUserInput admits only the stable composer identity retained by Codex.
// Source: codex-rs/protocol/src/protocol.rs:2669:2674@0df4aa677.
func ConfirmUserInput(ctx context.Context, thread, path, id string) error {
	if thread == "" || id == "" {
		return errors.New("human input reconciliation requires thread and client identity")
	}
	for line := range reverseRecords(thread, path) {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				ID   string `json:"client_id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		if record.Type == "event_msg" && record.Payload.Type == "user_message" && record.Payload.ID == id {
			return nil
		}
	}
	return errors.New("human input remains uncertain: exact retained client identity is unavailable")
}

// ConfirmTerminalTurn settles only an interruption of this exact host turn.
// A different turn's completion cannot acknowledge the requested interruption.
func ConfirmTerminalTurn(ctx context.Context, thread, path, turn string) error {
	if thread == "" || turn == "" {
		return errors.New("terminal reconciliation requires a thread and turn")
	}
	for line := range reverseRecords(thread, path) {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Turn string `json:"turn_id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		if record.Type == "event_msg" && record.Payload.Turn == turn {
			switch record.Payload.Type {
			case "task_complete", "turn_complete", "turn_aborted":
				return nil
			}
		}
	}
	return errors.New("interruption remains uncertain: exact terminal turn is unavailable")
}

// ConfirmRetainedOutput verifies the host's named, client-authored continuation.
// The caller supplies an exact output containing its durable dispatch identity.
// Source: codex-rs/history/src/tests.rs:351:387,516:561 and
// codex-rs/core/src/compact_retained_tool_outputs.rs@0df4aa677.
func ConfirmRetainedOutput(ctx context.Context, thread, path, namespace, name, output string) error {
	if thread == "" || namespace == "" || name == "" || output == "" {
		return errors.New("continuation reconciliation requires complete retained output")
	}
	type metadata struct {
		ClientAuthored bool `json:"client_authored"`
		Inherited      bool `json:"inherited_user_message"`
	}
	match := func(raw jsontext.Value, meta metadata) bool {
		var item struct {
			Type      string         `json:"type"`
			Call      *string        `json:"call_id"`
			Namespace string         `json:"namespace"`
			Name      string         `json:"name"`
			Output    jsontext.Value `json:"output"`
		}
		if !meta.ClientAuthored || meta.Inherited || json.Unmarshal(raw, &item) != nil || item.Type != "function_call_output" || item.Call != nil || item.Namespace != namespace || item.Name != name {
			return false
		}
		var text string
		return json.Unmarshal(item.Output, &text) == nil && text == output
	}
	for line := range reverseRecords(thread, path) {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record struct {
			Type     string         `json:"type"`
			Payload  jsontext.Value `json:"payload"`
			Metadata metadata       `json:"metadata"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		switch record.Type {
		case "response_item":
			if match(record.Payload, record.Metadata) {
				return nil
			}
		case "compacted":
			var compacted struct {
				History  []jsontext.Value `json:"replacement_history"`
				Metadata []metadata       `json:"replacement_history_metadata"`
			}
			if err := json.Unmarshal(record.Payload, &compacted); err != nil {
				return err
			}
			if len(compacted.History) == len(compacted.Metadata) {
				for i, item := range compacted.History {
					if match(item, compacted.Metadata[i]) {
						return nil
					}
				}
			}
		}
	}
	return errors.New("continuation remains uncertain: exact host-retained output is unavailable")
}
