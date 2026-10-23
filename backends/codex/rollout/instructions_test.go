package rollout

import (
	"bytes"
	"testing"
)

func TestInstructionItemsRetainsCompactionProvenance(t *testing.T) {
	developer := map[string]any{"type": "message", "id": "generated", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "role"}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"multi_agent.role_instructions"}}}
	source, _ := prefixFile(t, "thread", "thread",
		map[string]any{"type": "session_meta", "payload": map[string]any{"id": "thread"}},
		map[string]any{"type": "compacted", "payload": map[string]any{"replacement_history": []any{developer, map[string]any{"type": "message", "role": "developer", "content": "role"}}}},
	)
	items := InstructionItems("thread", source.Path)
	if len(items) != 1 || !bytes.Contains(items[0], []byte(`"id":"generated"`)) {
		t.Fatalf("compaction provenance = %s", items)
	}
	if items := InstructionItems("foreign", source.Path); len(items) != 0 {
		t.Fatalf("foreign provenance = %s", items)
	}
}
