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

func TestReferencesLiteralMarkers(t *testing.T) {
	text := "@ @@ @@@ @@name\n@@ -1,2 +3,4 @@\n`@` `@@` (@) [@@] {\"marker\":\"@@\"} \\@escaped @missing @./@ @\"@\" @'@name'"
	want := []string{"missing", "./@", "@", "@name"}
	if got := slices.Collect(References(text)); !slices.Equal(got, want) {
		t.Fatalf("references = %q, want %q", got, want)
	}
}
