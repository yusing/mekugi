package binding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/contracts/guidance"
)

func ReadReferencedInstructionFragments(ctx context.Context, thread, path string, resolve ForkResolver) ([]guidance.Fragment, error) {
	items, err := rollout.ReferencedInstructionItems(ctx, thread, path, resolve)
	if err != nil {
		return nil, err
	}
	var fragments []guidance.Fragment
	for _, item := range items {
		parts, err := NativeInstructionFragments(thread, item)
		if err != nil {
			return nil, err
		}
		fragments = append(fragments, parts...)
	}
	return fragments, nil
}

// NativeInstructionFragments decodes content-kind provenance from an admitted
// raw response item. The caller authenticates the containing host thread and
// retains these facts before a projection can depend on them.
// Source: codex-rs/protocol/src/models.rs@0df4aa677 ResponseItem metadata;
// codex-rs/app-server/src/bespoke_event_handling.rs:1167@0df4aa677.
func NativeInstructionFragments(thread string, item jsontext.Value) ([]guidance.Fragment, error) {
	if thread == "" {
		return nil, errors.New("instruction provenance requires an admitted thread")
	}
	var envelope struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Metadata struct {
			Kinds []*string `json:"content_item_kinds"`
		} `json:"internal_chat_message_metadata_passthrough"`
	}
	if err := json.Unmarshal(item, &envelope); err != nil {
		return nil, fmt.Errorf("decode instruction provenance: %w", err)
	}
	if envelope.Type != "message" || len(envelope.Metadata.Kinds) == 0 {
		return nil, nil
	}
	if envelope.ID == "" || len(envelope.Content) != len(envelope.Metadata.Kinds) {
		return nil, errors.New("instruction provenance has missing identity or mismatched content indices")
	}
	var result []guidance.Fragment
	for i, kind := range envelope.Metadata.Kinds {
		if kind == nil || *kind == "" {
			continue
		}
		part := envelope.Content[i]
		if part.Type != "input_text" && part.Type != "output_text" {
			continue
		}
		if (*kind == "multi_agent.role_instructions" || *kind == "multi_agent.mode_instructions" || *kind == "multi_agent.usage_hint") && envelope.Role != "developer" {
			return nil, errors.New("multi-agent instruction provenance is not developer content")
		}
		result = append(result, guidance.Fragment{Thread: thread, Item: envelope.ID, Index: i, Kind: *kind, Text: part.Text})
	}
	return result, nil
}

func ReadInstructionFragments(thread, path string) ([]guidance.Fragment, error) {
	var fragments []guidance.Fragment
	for _, item := range rollout.InstructionItems(thread, path) {
		parts, err := NativeInstructionFragments(thread, item)
		if err != nil {
			return nil, err
		}
		fragments = append(fragments, parts...)
	}
	return fragments, nil
}

// MatchInstructionFragment binds provider content to a previously admitted host
// fragment even when provider serialization omits the native metadata.
func MatchInstructionFragment(fragment guidance.Fragment, thread, item string, index int, text string) error {
	if thread == "" || item == "" || fragment.Thread != thread || fragment.Item != item || fragment.Index != index {
		return errors.New("instruction provenance identity mismatch")
	}
	if fragment.Text != text {
		return errors.New("instruction provenance content mismatch")
	}
	return nil
}
