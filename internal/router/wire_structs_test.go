package router

import (
	"encoding/json"
	"testing"
)

func TestAssistantCommentaryMessageWireJSON(t *testing.T) {
	got := mustMarshalJSON(assistantCommentaryMessage("msg_123", "Working.\nStill working."))
	want := `{"content":[{"annotations":[],"text":"Working.\nStill working.","type":"output_text"}],"id":"msg_123","phase":"commentary","role":"assistant","status":"completed","type":"message"}`
	if string(got) != want {
		t.Fatalf("commentary message JSON = %s, want %s", got, want)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 6 {
		t.Fatalf("commentary message fields = %v", fields)
	}
}

// The empty-value case was captured from the former SDK implementation,
// including omission of an empty ID.
func TestWireBuildersEmptyValues(t *testing.T) {
	for _, test := range []struct {
		name string
		got  map[string]json.RawMessage
		want string
	}{
		{"commentary", assistantCommentaryMessage("", ""), `{"content":[{"annotations":[],"text":"","type":"output_text"}],"phase":"commentary","role":"assistant","status":"completed","type":"message"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := string(mustMarshalJSON(test.got)); got != test.want {
				t.Fatalf("wire JSON = %s, want %s", got, test.want)
			}
		})
	}
}
