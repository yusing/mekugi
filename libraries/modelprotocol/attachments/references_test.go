package attachments

import (
	"slices"
	"testing"
)

func TestReferences(t *testing.T) {
	text := "@plain\n{\"sources\":[\"@sub/file.go\",\"@日本語\"]} (`@quoted`) @\"with spaces\" @'single space' user@example.com prefix@literal @"
	want := []string{"plain", "sub/file.go", "日本語", "quoted", "with spaces", "single space"}
	if got := slices.Collect(References(text)); !slices.Equal(got, want) {
		t.Fatalf("references = %q, want %q", got, want)
	}
}
