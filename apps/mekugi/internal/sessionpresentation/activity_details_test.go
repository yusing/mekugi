package sessionpresentation

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestDetailSelectionRetainsSourceAndRejectsStaleRequests(t *testing.T) {
	var attachment Attachment
	var details Details
	feeds := attachment.Activity()
	source := &ActivitySource{Model: &feeds.Main, Audience: MainAudience}
	retention := new(feed.Retention)
	output := retention.New()
	output.Write("first\n")
	entry := ActivityEntry{Seq: 1, Agent: "/root", Kind: "tool", Text: "Run `printf first`", Native: &NativeItem{Output: output, Running: true}}
	source.Apply(ActivityEvent{Entries: []ActivityEntry{entry}})
	pages := activity.DetailPages(source.Entries[0].Values)
	id := details.OpenDetails(Version{Attachment: 7}, source, pages, false, attachment.RunningProcesses())
	view, ok := details.DetailView(id)
	if !ok || len(view.Pages) != 1 || !view.Pages[0].Retained || !view.Pages[0].OutputLive {
		t.Fatalf("detail selection unavailable without terminal: %+v", view)
	}
	if revision, ok := details.RefreshDetails(id, nil, attachment.RunningProcesses()); !ok || revision != view.Revision {
		t.Fatal("unchanged detail refresh revised the publication")
	}
	request := DetailOutputRequest{ID: id, Page: view.Pages[0].ID, Version: view.Pages[0].OutputVersion}
	first, ok := details.DetailOutput(request)
	if !ok || strings.Join(first.View.Lines, "\n") != "first" {
		t.Fatalf("selected output unavailable: %+v", first)
	}
	first.View.Lines[0] = "changed copy"
	if output.View().Lines[0] != "first" {
		t.Fatal("published output aliases retained source")
	}
	source.Model.Remove(0, len(source.Entries))
	output.Write("second\n")
	if _, ok := details.DetailOutput(request); ok {
		t.Fatal("old output version read newer bytes")
	}
	unchanged, _ := details.DetailView(id)
	if unchanged.Pages[0].OutputVersion != request.Version {
		t.Fatal("view read refreshed source facts")
	}
	details.RefreshDetails(id, nil, attachment.RunningProcesses())
	view, _ = details.DetailView(id)
	request.Version = view.Pages[0].OutputVersion
	current, ok := details.DetailOutput(request)
	if !ok || strings.Join(current.View.Lines, "\n") != "first\nsecond" {
		t.Fatalf("feed aging retired selected output: %+v", current)
	}
	replacement := details.OpenDetails(Version{Attachment: 7}, source, pages, false, attachment.RunningProcesses())
	details.CloseDetails(id)
	if _, ok := details.DetailView(replacement); !ok {
		t.Fatal("stale close retired replacement")
	}
	if _, ok := details.DetailOutput(request); ok {
		t.Fatal("stale selection resolved replacement")
	}
	details.CloseDetails(replacement)
	if _, ok := details.DetailView(replacement); ok || output.View().Released {
		t.Fatal("close retained a selection or released another owner's output")
	}
	details.Clear()
	if _, ok := details.RefreshDetails(id, nil, attachment.RunningProcesses()); ok {
		t.Fatal("retirement revived detail selection")
	}
}

func TestDetailPreparedRevisionsStayStableUntilContentChanges(t *testing.T) {
	var attachment Attachment
	var details Details
	content := activity.PreparedContent{ID: "call", Text: "first", Altered: true}
	source := &ActivitySource{Model: &attachment.Activity().Main, Audience: MainAudience,
		Prepared: func(_, _ string) (activity.PreparedContent, bool) { return content, true }}
	source.Apply(ActivityEvent{Entries: []ActivityEntry{{Seq: 1, Kind: "tool", Text: "Run `echo original`", Native: &NativeItem{Thread: "thread", Item: "call"}}}})
	id := details.OpenDetails(Version{Attachment: 1}, source, activity.DetailPages(source.Entries[0].Values), false, attachment.RunningProcesses())
	var previous uint64
	for _, text := range []string{"first", "second", "third"} {
		content.Text = text
		details.RefreshDetails(id, nil, attachment.RunningProcesses())
		view, _ := details.DetailView(id)
		if len(view.Pages) != 1 || view.Pages[0].Prepared.Text != text || view.Pages[0].Revision <= previous {
			t.Fatalf("stale prepared publication: %+v", view)
		}
		previous = view.Pages[0].Revision
		if revision, _ := details.RefreshDetails(id, nil, attachment.RunningProcesses()); revision != view.Revision {
			t.Fatal("unchanged prepared content revised the publication")
		}
	}
	content.Altered = false
	details.RefreshDetails(id, nil, attachment.RunningProcesses())
	view, _ := details.DetailView(id)
	if view.Pages[0].Prepared != nil {
		t.Fatal("equal content kept the model-visible variant")
	}
}
