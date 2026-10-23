package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
)

// ThreadAdmissionPolicy is the invocation's final owned configuration overlay.
// It applies at the sole request owner, after caller/role/restored settings.
type ThreadAdmissionPolicy struct{}

func (p ThreadAdmissionPolicy) Apply(params any) (jsontext.Value, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var fields map[string]jsontext.Value
	if err = json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("thread admission requires object parameters")
	}
	config := make(map[string]jsontext.Value)
	if existing := fields["config"]; existing != nil && string(existing) != "null" {
		if err = json.Unmarshal(existing, &config); err != nil {
			return nil, err
		}
	}
	config["features.multi_agent_v2.enabled"] = jsontext.Value("true")
	config["features.multi_agent_v2.disable_native_tools"] = jsontext.Value("true")
	fields["config"], err = json.Marshal(config)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}
