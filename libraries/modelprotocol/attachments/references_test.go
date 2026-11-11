package attachments

import (
	json "encoding/json/v2"
	"reflect"
	"slices"
	"testing"
)

func TestReferenceProsePunctuation(t *testing.T) {
	for _, ref := range []string{
		"@file.md", "@file.md#L2-L4,L8-L10", `@"with spaces"#L2-L4,L8-L10`,
		"$guide", "$guide/references/file.md", "$guide/references/file.md#L2-L4,L8-L10",
		"mchanges:blue1,blue3..blue7,birch2..5",
	} {
		for _, punctuation := range []string{".", ",", ":", "!", "?", "?!", ";", ").", "]!"} {
			text := "日本語: " + ref + punctuation + " Next."
			tokens := slices.Collect(ReferenceTokens(text))
			if len(tokens) != 1 || tokens[0].Text != ref || text[tokens[0].Start:tokens[0].End] != ref {
				t.Fatalf("scan %q: %+v", text, tokens)
			}
			if _, err := ParseReference(tokens[0].Text); err != nil {
				t.Fatalf("parse %q: %v", tokens[0].Text, err)
			}
		}
	}
	for _, ref := range []string{`@"file.md."`, "@'file?!'", `@"with spaces."#L2-L4`} {
		if got := slices.Collect(References("Read " + ref + ".")); !slices.Equal(got, []string{ref}) {
			t.Fatalf("quoted reference %q: %q", ref, got)
		}
	}
	for _, ref := range []string{"@missing#L1-L2junk", "mchanges:blue1.."} {
		got := slices.Collect(References("Read " + ref + "!"))
		if !slices.Equal(got, []string{ref}) {
			t.Fatalf("malformed reference %q: %q", ref, got)
		}
		if _, err := ParseReference(got[0]); err == nil {
			t.Fatalf("malformed reference became valid: %q", got[0])
		}
	}
}

func TestReferenceExactSelectionsAndCanonicalPaths(t *testing.T) {
	for _, suffix := range []string{".", ",", ":", "!", "?", ".?!"} {
		want := Reference{Path: "file" + suffix}
		ref := "@" + want.Path
		got, err := ParseReference(ref)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("explicit selection %q: %+v %v", ref, got, err)
		}
		canonical := got.String()
		for _, text := range []string{canonical, "Read " + canonical + "."} {
			tokens := slices.Collect(References(text))
			if !slices.Equal(tokens, []string{canonical}) {
				t.Fatalf("canonical scan %q: %q", text, tokens)
			}
			parsed, err := ParseReference(tokens[0])
			if err != nil || !reflect.DeepEqual(parsed, want) {
				t.Fatalf("canonical parse %q: %+v %v", tokens[0], parsed, err)
			}
		}
	}
	for _, ref := range []string{"@file.", "@file#L2-L4", `@"with spaces."#L2-L4`, "$guide/reference.md.", "mchanges:blue1..3", "mchanges:blue1."} {
		encoded, err := json.Marshal(map[string][]string{"sources": {ref}})
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Collect(References(string(encoded))); !slices.Equal(got, []string{ref}) {
			t.Fatalf("exact JSON reference %q: %q", encoded, got)
		}
	}
}

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
