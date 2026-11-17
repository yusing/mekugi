// Source: apps/mekugi/internal/router/composer_file_attachment.go:1:116@b9679c93.
package requestexecution

import (
	"bytes"
	"encoding/json/jsontext"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	json "encoding/json/v2"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

// ProjectFileAttachments splits only complete versioned text parts. Malformed
// lookalikes remain ordinary user text; non-user messages are never interpreted.
func ProjectFileAttachments(request *codexresponses.Request) (bool, map[int]string) {
	identities := make(map[int]string)
	if !bytes.Contains(request.Fields["input"], []byte("mekugi-file-attachments-v")) {
		return false, nil
	}
	var items []map[string]jsontext.Value
	if json.Unmarshal(request.Fields["input"], &items) != nil {
		return false, nil
	}
	var projected []map[string]jsontext.Value
	changed := false

	for _, item := range items {
		var role string
		_ = json.Unmarshal(item["role"], &role)
		var parts []map[string]jsontext.Value
		if role != "user" || json.Unmarshal(item["content"], &parts) != nil {
			projected = append(projected, item)
			continue
		}
		var kept []map[string]jsontext.Value
		var attached []string
		var attachedIDs []string
		for _, part := range parts {
			var kind, text string
			_ = json.Unmarshal(part["type"], &kind)
			_ = json.Unmarshal(part["text"], &text)
			snapshot, ok := attachments.DecodeSnapshot(text)
			if kind == "input_text" && ok {
				attached = append(attached, snapshot.Frames...)
				for _, frame := range snapshot.Frames {
					attachedIDs = append(attachedIDs, snapshot.ReceiptID(frame))
				}
			} else {
				kept = append(kept, part)
			}
		}
		if len(attached) == 0 {
			if len(kept) == 0 {
				continue
			}
			if len(kept) != len(parts) {
				item["content"], _ = json.Marshal(kept)
				delete(item, "internal_chat_message_metadata_passthrough")
			}
			projected = append(projected, item)
			continue
		}
		changed = true
		if len(kept) > 0 {
			item["content"], _ = json.Marshal(kept)
			// Content-kind metadata refers to the original part indices.
			delete(item, "internal_chat_message_metadata_passthrough")
			projected = append(projected, item)
		}
		for i, frame := range attached {
			if attachedIDs[i] != "" {
				identities[len(projected)] = attachedIDs[i]
			}
			content, _ := json.Marshal([]map[string]string{{"type": "input_text", "text": frame}})
			projected = append(projected, map[string]jsontext.Value{"type": []byte(`"message"`), "role": []byte(`"user"`), "content": content})
		}
	}
	if changed {
		input, _ := json.Marshal(projected)
		request.Fields["input"] = input
	}
	return changed, identities
}
