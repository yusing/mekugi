package sessionpresentation

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestDetailSelectionRetainsSourceAndRejectsStaleRequests(t *testing.T) {
	var attachment Attachment
	feeds := attachment.Activity()
	source := &ActivitySource{Model: &feeds.Main, Audience: MainAudience}
	retention := new(feed.Retention)
	output := retention.New()
	output.Write("first\n")
	entry := ActivityEntry{Seq: 1, Agent: "/root", Kind: "tool", Text: "Run `printf first`", Native: &NativeItem{Output: output, Running: true}}
	source.Apply(ActivityEvent{Entries: []ActivityEntry{entry}})
	pages := activity.DetailPages(source.Entries[0].Values)
	id := attachment.OpenDetails(Version{Attachment: 7}, source, pages, false)
	view, ok := attachment.DetailView(id)
	if !ok || len(view.Pages) != 1 || !view.Pages[0].Retained || !view.Pages[0].OutputLive {
		t.Fatalf("detail selection unavailable without terminal: %+v", view)
	}
	if revision, ok := attachment.RefreshDetails(id, nil); !ok || revision != view.Revision {
		t.Fatal("unchanged detail refresh revised the publication")
	}
	request := DetailOutputRequest{ID: id, Page: view.Pages[0].ID, Version: view.Pages[0].OutputVersion}
	first, ok := attachment.DetailOutput(request)
	if !ok || strings.Join(first.View.Lines, "\n") != "first" {
		t.Fatalf("selected output unavailable: %+v", first)
	}
	first.View.Lines[0] = "changed copy"
	if output.View().Lines[0] != "first" {
		t.Fatal("published output aliases retained source")
	}
	source.Model.Remove(0, len(source.Entries))
	output.Write("second\n")
	if _, ok := attachment.DetailOutput(request); ok {
		t.Fatal("old output version read newer bytes")
	}
	unchanged, _ := attachment.DetailView(id)
	if unchanged.Pages[0].OutputVersion != request.Version {
		t.Fatal("view read refreshed source facts")
	}
	attachment.RefreshDetails(id, nil)
	view, _ = attachment.DetailView(id)
	request.Version = view.Pages[0].OutputVersion
	current, ok := attachment.DetailOutput(request)
	if !ok || strings.Join(current.View.Lines, "\n") != "first\nsecond" {
		t.Fatalf("feed aging retired selected output: %+v", current)
	}
	replacement := attachment.OpenDetails(Version{Attachment: 7}, source, pages, false)
	attachment.CloseDetails(id)
	if _, ok := attachment.DetailView(replacement); !ok {
		t.Fatal("stale close retired replacement")
	}
	if _, ok := attachment.DetailOutput(request); ok {
		t.Fatal("stale selection resolved replacement")
	}
	attachment.CloseDetails(replacement)
	if _, ok := attachment.DetailView(replacement); ok || output.View().Released {
		t.Fatal("close retained a selection or released another owner's output")
	}
	attachment.Retire()
	if _, ok := attachment.RefreshDetails(id, nil); ok {
		t.Fatal("retirement revived detail selection")
	}
}

func TestDetailPreparedRevisionsStayStableUntilContentChanges(t *testing.T) {
	var attachment Attachment
	content := activity.PreparedContent{ID: "call", Text: "first", Altered: true}
	source := &ActivitySource{Model: &attachment.Activity().Main, Audience: MainAudience,
		Prepared: func(_, _ string) (activity.PreparedContent, bool) { return content, true }}
	source.Apply(ActivityEvent{Entries: []ActivityEntry{{Seq: 1, Kind: "tool", Text: "Run `echo original`", Native: &NativeItem{Thread: "thread", Item: "call"}}}})
	id := attachment.OpenDetails(Version{Attachment: 1}, source, activity.DetailPages(source.Entries[0].Values), false)
	var previous uint64
	for _, text := range []string{"first", "second", "third"} {
		content.Text = text
		attachment.RefreshDetails(id, nil)
		view, _ := attachment.DetailView(id)
		if len(view.Pages) != 1 || view.Pages[0].Prepared.Text != text || view.Pages[0].Revision <= previous {
			t.Fatalf("stale prepared publication: %+v", view)
		}
		previous = view.Pages[0].Revision
		if revision, _ := attachment.RefreshDetails(id, nil); revision != view.Revision {
			t.Fatal("unchanged prepared content revised the publication")
		}
	}
	content.Altered = false
	attachment.RefreshDetails(id, nil)
	view, _ := attachment.DetailView(id)
	if view.Pages[0].Prepared != nil {
		t.Fatal("equal content kept the model-visible variant")
	}
}
