package codexbinding

import (
	"bytes"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// ProjectMessageIDs keeps retained host identities out of provider input. These
// local IDs exceed the provider's 64-character limit and are not item references.
// Call after local evidence consumers, which still need the original identities.
func ProjectMessageIDs(input []byte) ([]byte, error) {
	if !bytes.Contains(input, []byte("threadcontrol_")) {
		return input, nil
	}
	var items []jsontext.Value
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, err
	}
	changed := false
	for i, raw := range items {
		if !bytes.Contains(raw, []byte("threadcontrol_")) {
			continue
		}
		var item map[string]jsontext.Value
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		id, owned := strings.CutPrefix(responses.StringField(item, "id"), "threadcontrol_")
		if !owned || len(id) != 64 || responses.StringField(item, "type") != "message" || responses.StringField(item, "role") != "assistant" {
			continue
		}
		if _, err := hex.DecodeString(id); err != nil {
			continue
		}
		delete(item, "id")
		items[i] = responses.MustMarshalJSON(item)
		changed = true
	}
	if changed {
		input = responses.MustMarshalJSON(items)
	}
	return input, nil
}
