package router

import "testing"

func TestAssistantCommentaryMessageWireJSON(t *testing.T) {
	got := mustMarshalJSON(assistantCommentaryMessage("msg_123", "Working.\nStill working."))
	want := `{"content":[{"annotations":[],"text":"Working.\nStill working.","type":"output_text"}],"id":"msg_123","phase":"commentary","role":"assistant","status":"completed","type":"message"}`
	if string(got) != want {
		t.Fatalf("commentary message JSON = %s, want %s", got, want)
	}
}

// The empty-value case was captured from the former SDK implementation,
// including omission of an empty ID.
func TestWireBuildersEmptyValues(t *testing.T) {
	got := string(mustMarshalJSON(assistantCommentaryMessage("", "")))
	want := `{"content":[{"annotations":[],"text":"","type":"output_text"}],"phase":"commentary","role":"assistant","status":"completed","type":"message"}`
	if got != want {
		t.Fatalf("wire JSON = %s, want %s", got, want)
	}
}
