package responses

import (
	jsonv1 "encoding/json"
	"testing"
)

func TestResponseContractPreservesPresenceAndDetachedBytes(t *testing.T) {
	for _, source := range []string{"", "null", `[{"type":"custom","name":"exec"}]`} {
		fields := make(map[string]jsonv1.RawMessage)
		for _, name := range []string{"tools", "tool_choice"} {
			if source != "" {
				fields[name] = jsonv1.RawMessage(source)
			}
		}
		contract := CaptureResponseContract(fields)
		for _, raw := range fields {
			raw[0] = ' '
		}
		for range 2 {
			response := map[string]jsonv1.RawMessage{"tools": []byte(`[]`), "tool_choice": []byte(`"auto"`), "status": []byte(`"completed"`)}
			contract.Restore(response)
			for _, name := range []string{"tools", "tool_choice"} {
				raw, present := response[name]
				if present != (source != "") || string(raw) != source {
					t.Fatalf("%s: got %s present=%v, want %q", name, raw, present, source)
				}
				if present {
					raw[0] = ' '
				}
			}
			if string(response["status"]) != `"completed"` {
				t.Fatal("unrelated response field changed")
			}
		}
		absent := make(map[string]jsonv1.RawMessage)
		contract.Restore(absent)
		if len(absent) != 0 {
			t.Fatal("restoration invented absent response fields")
		}
	}
}
