package sessionpresentation

import (
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestAttachmentParametersCoverSelectionsAndOmissions(t *testing.T) {
	frames := composer.FileFrames("/workspace/file", "file body")
	frames = append(frames, composer.SkillFrames("review", "", "skill body")...)
	frames = append(frames,
		attachments.FormatSelection("[Selected text]", "screen", "first "),
		attachments.FormatSelection("[Selected text]", "screen", "second"),
		`Attached skill "denied": CONTENT NOT ATTACHED ("permission denied"). Read separately.`,
		"Attached skill references: remaining contents NOT ATTACHED (attachment budget exceeded). Read remaining referenced skills separately if needed.",
		"Attached file references: remaining contents NOT ATTACHED (attachment budget exceeded). Read remaining referenced files separately if needed.",
	)
	var parts []map[string]string
	for _, id := range []string{"first", "second"} {
		text := (attachments.Snapshot{ID: id, Frames: frames}).Encode()
		parts = append(parts, map[string]string{"type": "text", "text": text})
	}
	content, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	values := AttachmentValues("/workspace", content)
	if len(values) != 12 || values[2].Verb != "Attached selection" || strings.Join(values[2].Tail, "\n") != "first second" || values[2].AttachmentID == "" || values[2].AttachmentID == values[8].AttachmentID {
		t.Fatalf("source snapshots/chunks lost: %+v", values)
	}
	event := activity.AttachmentEvents(values)
	if len(event) != 1 || event[0].Label != "2 files, 4 skills, 2 selections, 4 omissions" {
		t.Fatalf("source kinds/omissions miscounted: %+v", event)
	}
}
