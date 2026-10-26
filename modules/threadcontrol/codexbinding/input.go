package codexbinding

import (
	"bytes"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// ProjectLocalIdentities keeps retained message and wake-dispatch identities out
// of provider input, without changing the saved history used for reconciliation.
// Call after local evidence consumers, which still need the original identities.
func ProjectLocalIdentities(input []byte) ([]byte, error) {
	if !bytes.Contains(input, []byte("threadcontrol")) {
		return input, nil
	}
	var items []jsontext.Value
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, err
	}
	changed := false
	for i, raw := range items {
		if !bytes.Contains(raw, []byte("threadcontrol")) {
			continue
		}
		var item map[string]jsontext.Value
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		if responses.StringField(item, "type") == "function_call_output" && (len(item["call_id"]) == 0 || item["call_id"].Kind() == 'n') && responses.StringField(item, "namespace") == "threadcontrol" && responses.StringField(item, "name") == "wait_agent" {
			var output map[string]jsontext.Value
			if json.Unmarshal([]byte(responses.StringField(item, "output")), &output) != nil || output["id"] == nil {
				continue
			}
			delete(output, "id")
			item["output"] = responses.MustMarshalJSON(string(responses.MustMarshalJSON(output)))
		} else {
			id, owned := strings.CutPrefix(responses.StringField(item, "id"), "threadcontrol_")
			if !owned || len(id) != 64 || responses.StringField(item, "type") != "message" || responses.StringField(item, "role") != "assistant" {
				continue
			}
			if _, err := hex.DecodeString(id); err != nil {
				continue
			}
			delete(item, "id")
		}
		items[i] = responses.MustMarshalJSON(item)
		changed = true
	}
	if changed {
		input = responses.MustMarshalJSON(items)
	}
	return input, nil
}
