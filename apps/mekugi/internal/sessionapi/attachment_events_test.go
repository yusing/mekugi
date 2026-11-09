package sessionapi

import (
	"strings"
	"testing"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestAttachmentEventProjectionAndContent(t *testing.T) {
	values := activity.AttachmentEvents([]activity.Value{
		{Kind: "op", Verb: "Attached", Path: "source.go", Tail: []string{strings.Repeat("source", 200), "end"}},
		{Kind: "op", Verb: "Attach failed", Path: "missing", Label: "not found"},
		{Kind: "op", Verb: "Attached skill", Label: "review", Tail: []string{"# Review"}},
	})
	full := projectEntry(activity.Entry{Facts: activity.Facts{Seq: 1, Kind: "attachments"}, Values: values}, false, "/root", "/root")
	if len(full.Values) != 1 || len(full.Values[0].Params) != 3 || full.Values[0].Params[1].Label != "not found" {
		t.Fatalf("missing event parameters: %+v", full.Values)
	}
	compact := compactEntry(full)
	params := compact.Values[0].Params
	if len(params) != 3 || len(params[0].Tail) != 0 || params[0].TailLines != 2 || len(full.Values[0].Params[0].Tail) != 2 {
		t.Fatalf("content compaction lost source/recovery: %+v", compact)
	}
}
