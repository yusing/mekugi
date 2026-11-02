package responses

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"
)

func TestLowReasoningPreservesPrefixAndUsesLatestEffort(t *testing.T) {
	for _, effort := range []string{"high", "low", "minimal", "none"} {
		t.Run(effort, func(t *testing.T) {
			request, err := ParseRequest([]byte(`{"model":"gpt-6-astra","reasoning":{"effort":"low","summary":"auto"},"input":[{"role":"user","content":"history"},{"type":"configuration_update","reasoning":{"effort":"max"}},{"type":"configuration_update","reasoning":{"effort":"` + effort + `"}},{"role":"user","content":"question"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			var before, after []jsontext.Value
			if err := json.Unmarshal(request.Fields["input"], &before); err != nil {
				t.Fatal(err)
			}
			if err := request.UseLowReasoning("high", []string{"low", "high"}, true); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(request.Fields["input"], &after); err != nil {
				t.Fatal(err)
			}
			want := len(before)
			if effort == "high" {
				want++
			}
			if len(after) != want || request.ReasoningEffort() != "low" {
				t.Fatalf("request changed: %+v", request.Fields)
			}
			for i := range before {
				if !bytes.Equal(before[i], after[i]) {
					t.Fatalf("prefix item %d changed", i)
				}
			}
			if effort == "high" && !bytes.Contains(after[len(after)-1], []byte(`"effort":"low"`)) {
				t.Fatal("missing low-effort update")
			}
		})
	}
}
