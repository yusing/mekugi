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
		if responses.StringField(item, "type") == "function_call_output" && (len(item["call_id"]) == 0 || item["call_id"].Kind() == 'n') && responses.StringField(item, "namespace") == "threadcontrol" {
			text := responses.StringField(item, "output")
			switch responses.StringField(item, "name") {
			case "wait_agent":
				var output map[string]jsontext.Value
				if json.Unmarshal([]byte(text), &output) != nil || output["id"] == nil {
					continue
				}
				delete(output, "id")
				if _, err := projectResultFields(output); err != nil {
					return nil, err
				}
				text = string(responses.MustMarshalJSON(output))
			case "followup_task":
				text = visibleMessageNames(text)
			case "completion_guard":
				text = visibleCompletionNames(text)
			default:
				continue
			}
			if text == responses.StringField(item, "output") {
				continue
			}
			item["output"] = responses.MustMarshalJSON(text)
		} else {
			id, owned := strings.CutPrefix(responses.StringField(item, "id"), "threadcontrol_")
			if !owned || len(id) != 64 || responses.StringField(item, "type") != "message" || responses.StringField(item, "role") != "assistant" {
				continue
			}
			if _, err := hex.DecodeString(id); err != nil {
				continue
			}
			delete(item, "id")
			var content []map[string]jsontext.Value
			if json.Unmarshal(item["content"], &content) == nil {
				for _, part := range content {
					if text := responses.StringField(part, "text"); text != "" {
						part["text"] = responses.MustMarshalJSON(visibleMessageNames(text))
					}
				}
				item["content"] = responses.MustMarshalJSON(content)
			}
		}
		items[i] = responses.MustMarshalJSON(item)
		changed = true
	}
	if changed {
		input = responses.MustMarshalJSON(items)
	}
	return input, nil
}
