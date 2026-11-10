package attachments

import (
	"reflect"
	"slices"
	"testing"
)

func TestReferences(t *testing.T) {
	text := "@plain\n{\"sources\":[\"@sub/file.go\",\"@日本語\"]} (`@quoted`) @\"with spaces\" @'single space' user@example.com prefix@literal @"
	want := []string{"@plain", "@sub/file.go", "@日本語", "@quoted", `@"with spaces"`, "@'single space'"}
	if got := slices.Collect(References(text)); !slices.Equal(got, want) {
		t.Fatalf("references = %q, want %q", got, want)
	}
}

func TestReferencesLiteralMarkers(t *testing.T) {
	text := "@ @@ @@@ @@name\n@@ -1,2 +3,4 @@\n`@` `@@` (@) [@@] {\"marker\":\"@@\"} \\@escaped @missing @./@ @\"@\" @'@name'"
	want := []string{"@missing", "@./@", `@"@"`, "@'@name'"}
	if got := slices.Collect(References(text)); !slices.Equal(got, want) {
		t.Fatalf("references = %q, want %q", got, want)
	}
}

func TestReferenceGrammarAcrossSelectionsAndText(t *testing.T) {
	for _, tc := range []struct {
		text string
		want Reference
	}{
		{"@file", Reference{Path: "file"}},
		{"@file#L2-L4", Reference{Path: "file", Ranges: [][2]int{{2, 4}}}},
		{"@file#L2", Reference{Path: "file", Ranges: [][2]int{{2, 2}}}},
		{"@'with spaces'#L2-L4,L8-L9", Reference{Path: "with spaces", Ranges: [][2]int{{2, 4}, {8, 9}}}},
		{"$guide", Reference{Skill: "guide"}},
		{"$guide/references/file.md", Reference{Skill: "guide/references/file.md"}},
		{"$guide/references/file.md#L3-L5", Reference{Skill: "guide/references/file.md", Ranges: [][2]int{{3, 5}}}},
		{"mchanges:amber1..3,birch2", Reference{Changes: []string{"amber1..3", "birch2"}}},
		{"mchanges:blue1,blue3..blue7,blue10,birch2..5", Reference{Changes: []string{"blue1", "blue3..blue7", "blue10", "birch2..5"}}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := ParseReference(tc.text)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parse: %+v %v", got, err)
			}
			for _, surrounding := range []string{tc.text, "Use `" + tc.text + "` now", `{"source":"` + tc.text + `"}`} {
				refs := slices.Collect(References(surrounding))
				if len(refs) != 1 || refs[0] != tc.text {
					t.Fatalf("scan %q: %q", surrounding, refs)
				}
			}
			roundtrip, err := ParseReference(got.String())
			if err != nil || !reflect.DeepEqual(roundtrip, got) {
				t.Fatalf("roundtrip %q: %+v %v", got.String(), roundtrip, err)
			}
		})
	}
}

func TestReferenceMalformedAndLiteralNeighbors(t *testing.T) {
	for _, text := range []string{"file", "@", "@''", "@x#L0-L2", "@x#L3-L2", "@x#L1-L", "@x#L01-L2", "@x#L1-L2junk", "$", "$guide/../private", "$guide//file", "$-flag", "mchanges:", "mchanges:--mine", "mchanges:amber1,", "mchanges:amber1..", "mchanges:amber1;touch"} {
		if _, err := ParseReference(text); err == nil {
			t.Errorf("accepted malformed %q", text)
		}
	}
	text := `\@literal \$literal \mchanges:amber1 email@example.com prefix$skill @@patch ${var} $(cmd) $skill @file#L1-L2 mchanges:amber1`
	want := []string{"$skill", "@file#L1-L2", "mchanges:amber1"}
	if got := slices.Collect(References(text)); !slices.Equal(got, want) {
		t.Fatalf("literal neighbors: %q", got)
	}
}
