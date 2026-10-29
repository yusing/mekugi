package sessionpresentation

import (
	json "encoding/json/v2"
	"slices"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestAttachmentValuesNameSkillsWithoutSourcePaths(t *testing.T) {
	frames := slices.Concat(
		composer.SkillFrames("review", "", "# Review\n\nCheck callers.\n"),
		composer.SkillFrames("commit", "", "# Commit\n"),
		composer.SkillFrames("guide", "/workspace/SKILL.md", "# Guide\n"),
	)
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": attachments.Encode(frames)}})
	if err != nil {
		t.Fatal(err)
	}
	values := AttachmentValues("/workspace", content)
	if len(values) != 3 {
		t.Fatalf("values = %+v", values)
	}
	for i, want := range []struct {
		label, path string
		tail        []string
	}{
		{"review", "", []string{"# Review", "", "Check callers.", ""}},
		{"commit", "", []string{"# Commit", ""}},
		{"guide", "/workspace/SKILL.md", []string{"# Guide", ""}},
	} {
		value := values[i]
		if value.Verb != "Attached skill" || value.Label != want.label || !slices.Equal(value.Tail, want.tail) {
			t.Fatalf("value %d = %+v", i, value)
		}
		// A skill without a source path has no read target, not an empty one.
		if want.path == "" && value.Reads != nil || want.path != "" && (len(value.Reads) != 1 || value.Reads[0].Path != want.path) {
			t.Fatalf("value %d reads = %+v", i, value.Reads)
		}
	}
}
