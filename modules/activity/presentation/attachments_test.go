package presentation

import (
	"strings"
	"testing"
)

func TestAttachmentEventRetainsParameters(t *testing.T) {
	values := []Value{
		{Kind: "op", Verb: "Attached", Path: "a.go", AttachmentID: "first", Tail: []string{"original"}},
		{Kind: "op", Verb: "Attached", Path: "a.go", AttachmentID: "second", Tail: []string{"new snapshot"}, Repeated: "same as O1"},
		{Kind: "op", Verb: "Attach failed", Path: "missing", Label: "not found"},
		{Kind: "op", Verb: "Attached skill", Label: "review", Tail: []string{"# Review"}},
		{Kind: "op", Verb: "Attached changes", Label: "amber1..3"},
		{Kind: "op", Verb: "Attached selection", Label: "Selected text", Tail: []string{"selected"}},
	}
	Identify(values, 12)
	record := Record[Entry]{Source: Entry{Facts: Facts{Seq: 12, Kind: "attachments"}}, Values: values}
	event := record.Publish()
	if len(event.Values) != 1 || event.Values[0].Kind != "attachments" || event.Values[0].Label != "3 files, 1 skill, 1 change, 1 selection" || !event.Values[0].Failed {
		t.Fatalf("not one counted event: %+v", event.Values)
	}
	pages := DetailPages(event.Values)
	if len(pages) != len(values) || pages[0].AttachmentID != "first" || pages[1].AttachmentID != "second" || pages[1].Repeated != "same as O1" || pages[0].ID == pages[1].ID {
		t.Fatalf("lost source identities: %+v", pages)
	}
	values[0].Tail = []string{"refreshed"}
	updated := record.Publish()
	pages = RefreshDetails(pages, map[uint64]DetailSource{12: {Entry: updated}})
	if strings.Join(pages[0].Tail, "") != "refreshed" || strings.Join(pages[1].Tail, "") != "new snapshot" {
		t.Fatalf("refresh crossed parameters: %+v", pages)
	}
	if got := SkillLoads(Record[Entry]{Source: event, Values: event.Values}); len(got) != 1 || got[0] != "review" {
		t.Fatalf("skill load lost: %v", got)
	}
	compact := CompactRoster([]Entry{event}, "/root")
	if len(compact) != 1 || compact[0].Verb != "Attach" || compact[0].Label != event.Values[0].Label || len(compact[0].Members) != 0 {
		t.Fatalf("roster retained contents or repeated actions: %+v", compact)
	}
}
