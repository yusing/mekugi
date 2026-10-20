package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
)

// CodeModeOnlyModelCatalog enforces the execution interface in model metadata,
// which Codex selects before consulting its execution feature flags.
func CodeModeOnlyModelCatalog(body []byte) ([]byte, error) {
	var catalog map[string]jsontext.Value
	if err := json.Unmarshal(body, &catalog); err != nil || catalog == nil {
		return nil, errors.New("invalid Codex model catalog")
	}
	var models []map[string]jsontext.Value
	if err := json.Unmarshal(catalog["models"], &models); err != nil || models == nil {
		return nil, errors.New("invalid Codex model catalog models")
	}
	changed := false
	for _, model := range models {
		if model == nil {
			return nil, errors.New("invalid Codex model catalog entry")
		}
		if string(model["tool_mode"]) != `"code_mode_only"` {
			model["tool_mode"] = jsontext.Value(`"code_mode_only"`)
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	encoded, err := json.Marshal(&models, json.Deterministic(true))
	if err != nil {
		return nil, err
	}
	catalog["models"] = encoded
	return json.Marshal(&catalog, json.Deterministic(true))
}
