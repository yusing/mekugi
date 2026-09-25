package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
)

// Explicitly opt out of automatic Daybreak selection without changing other
// access programs or the selected model.
func standardCyberAccess(raw []byte) ([]byte, error) {
	programs := make(map[string]jsontext.Value)
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &programs); err != nil {
			return nil, fmt.Errorf("invalid access_programs: %w", err)
		}
	}
	if programs == nil {
		programs = make(map[string]jsontext.Value)
	}
	programs["cyber"] = jsontext.Value(`"standard"`)
	return json.Marshal(programs)
}
