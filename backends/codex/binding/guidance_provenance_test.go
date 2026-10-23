package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"
)

func TestNativeInstructionProvenance(t *testing.T) {
	// Shape captured from Codex 0.163.0 rawResponseItem/completed while an
	// independent root was blocked in a stock JavaScript MCP invocation.
	const role = `<multi_agent_role>Use native role policy.</multi_agent_role>`
	item := map[string]any{"type": "message", "id": "msg_root_role", "role": "developer",
		"content": []any{map[string]any{"type": "input_text", "text": role}},
		"internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []any{"multi_agent.role_instructions"}},
	}
	data, err := json.Marshal(&item)
	if err != nil {
		t.Fatal(err)
	}
	fragments, err := NativeInstructionFragments("thread", data)
	if err != nil || len(fragments) != 1 {
		t.Fatalf("fragments=%v, error=%v", fragments, err)
	}
	f := fragments[0]
	if f.Kind != "multi_agent.role_instructions" || f.Text != role {
		t.Fatalf("wrong native fragment: %+v", f)
	}
	delete(item, "internal_chat_message_metadata_passthrough")
	providerData, err := json.Marshal(&item)
	if err != nil {
		t.Fatal(err)
	}
	lookalikes, err := NativeInstructionFragments("thread", providerData)
	if err != nil || len(lookalikes) != 0 {
		t.Fatalf("untagged prose acquired provenance: %v, %v", lookalikes, err)
	}
	if err := MatchInstructionFragment(f, "thread", "msg_root_role", 0, role); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, thread, item, text string
		index                    int
	}{
		{"foreign-thread", "other", f.Item, role, 0},
		{"caller-lookalike", f.Thread, "caller-authored", role, 0},
		{"different-part", f.Thread, f.Item, role, 1},
		{"modified-policy", f.Thread, f.Item, role + " changed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := MatchInstructionFragment(f, tc.thread, tc.item, tc.index, tc.text); err == nil {
				t.Fatal("accepted mismatched provenance")
			}
		})
	}
}

func TestNativeInstructionProvenanceRejectsMisalignedKinds(t *testing.T) {
	for _, raw := range []string{
		`{"type":"message","id":"item","role":"developer","content":[],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.role_instructions"]}}`,
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":"policy"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.role_instructions"]}}`,
		`{"type":"message","id":"item","role":"user","content":[{"type":"input_text","text":"policy"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["multi_agent.role_instructions"]}}`,
	} {
		if _, err := NativeInstructionFragments("thread", jsontext.Value(raw)); err == nil {
			t.Fatal("accepted malformed provenance")
		}
	}
}

func TestDecodeMCPIdentity(t *testing.T) {
	meta := map[string]any{"threadId": "thread", "sessionId": "session", "callId": "nested-call", "itemId": "exec-item", "windowId": "window",
		"x-codex-turn-metadata": map[string]any{"thread_id": "thread", "session_id": "session", "turn_id": "turn", "codex_version": "0.163.0", "model": "model", "reasoning_effort": "low"}}
	data, _ := json.Marshal(&meta)
	got, err := DecodeMCPIdentity(data)
	if err != nil || got.Thread != "thread" || got.Call != "nested-call" || got.Item != "exec-item" || got.Turn != "turn" || got.Effort != "low" {
		t.Fatalf("identity=%+v error=%v", got, err)
	}
	for _, field := range []string{"threadId", "sessionId", "callId", "itemId", "windowId", "x-codex-turn-metadata"} {
		old := meta[field]
		delete(meta, field)
		data, _ := json.Marshal(&meta)
		if _, err := DecodeMCPIdentity(data); err == nil {
			t.Errorf("accepted missing %s", field)
		}
		meta[field] = old
	}
	meta["threadId"] = "forged"
	data, _ = json.Marshal(&meta)
	if _, err := DecodeMCPIdentity(data); err == nil {
		t.Fatal("accepted conflicting thread metadata")
	}
}
