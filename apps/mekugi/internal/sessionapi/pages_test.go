package sessionapi

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	activityfeed "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestShellPreviewPreservesFullScript(t *testing.T) {
	source := "set -e\nplane comment ITEM --body - <<'EOF'\n" + strings.Repeat("retained summary\n", 100) + "EOF\nplane update ITEM"
	item := activity.ItemContent{Kind: "tool", Text: activity.ShellSource(source, "")}
	full := projectEntry(activity.Entry{Facts: item.Facts, Values: item.Values()}, false, "/root")
	for _, entry := range []entry{full, compactEntry(full)} {
		if v := entry.Values[0]; v.CodePreview != "plane comment ITEM --body - <<'EOF'" {
			t.Fatalf("preview=%+v", v)
		}
	}
	if full.Values[0].Code != source {
		t.Fatalf("full script changed: %q", full.Values[0].Code)
	}
}

func readSharedLive(t *testing.T, server *Server, event map[string]jsontext.Value) jsontext.Value {
	t.Helper()
	var refs DeferredSections
	if err := json.Unmarshal(event["deferred"], &refs); err != nil {
		t.Fatal(err)
	}
	if refs.Live == nil {
		return event["live"]
	}
	result, failure := server.content(testJSON(t, refs.Live))
	if failure != nil {
		t.Fatal(failure)
	}
	return testJSON(t, result.(ContentResult).Live)
}

func TestRosterAndAuxiliaryContentRemainReadable(t *testing.T) {
	s := NewLive()
	agents := &activity.Publication{}
	for i := range 120 {
		agents.Roster = append(agents.Roster, activity.AgentRow{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: fmt.Sprintf("/root/worker%d", i)}}})
	}
	frame := Frame{Session: Session{Title: strings.Repeat("large title ", 5000)}, Agents: agents, Input: &Input{Reason: strings.Repeat("input reason ", 5000)}}
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.snapshot()
	if err != nil || len(snapshot.Data) > 30<<10 {
		t.Fatalf("bounded auxiliary publication: %v", err)
	}
	var initial struct {
		Deferred DeferredSections `json:"deferred"`
		Roster   roster           `json:"roster"`
	}
	if err := json.Unmarshal(snapshot.Data, &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Deferred.Session == nil || initial.Deferred.Input == nil || initial.Roster.Next == nil {
		t.Fatal("large sections and roster did not publish continuation references")
	}
	for _, ref := range []*ContentReference{initial.Deferred.Session, initial.Deferred.Input} {
		value, failure := s.content(testJSON(t, ref))
		if failure != nil {
			t.Fatal(failure)
		}
		full := value.(ContentResult)
		if ref.Kind == "session" && full.Session.Title != frame.Session.Title || ref.Kind == "input" && full.Input.Reason != frame.Input.Reason {
			t.Fatal("deferred section lost complete content")
		}
	}
	seen, next := len(initial.Roster.Agents), initial.Roster.Next
	for next != nil {
		value, failure := s.page(testJSON(t, next))
		if failure != nil {
			t.Fatal(failure)
		}
		page := value.(PageResult)
		seen += len(page.Roster.Agents)
		next = page.Roster.Next
	}
	if seen != 120 {
		t.Fatalf("roster retained %d agents", seen)
	}
	frame.Session.Title = "small"
	frame.Input = &Input{}
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if _, failure := s.content(testJSON(t, initial.Deferred.Session)); failure == nil || failure.Data.Status != 409 {
		t.Fatal("stale section remained readable")
	}
	snapshot, err = s.snapshot()
	initial.Deferred = DeferredSections{}
	if err != nil || json.Unmarshal(snapshot.Data, &initial) != nil || initial.Deferred.Session != nil || initial.Deferred.Input != nil {
		t.Fatal("small sections remained deferred")
	}
}

func TestRetainedFeedPagesAndContent(t *testing.T) {
	t.Parallel()
	s := New()
	publication := &activity.Publication{}
	for i := range 600 {
		publication.Entries = append(publication.Entries, activity.Entry{Seq: uint64(i + 1), Agent: "Main", Kind: "text", Text: strings.Repeat("retained text ", 400), Revision: 1})
	}
	frame := Frame{Session: Session{Thread: "large"}, Main: publication}
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.snapshot()
	if err != nil || len(snapshot.Data) > 32<<10 {
		t.Fatalf("bounded snapshot: %d bytes, %v", len(snapshot.Data), err)
	}
	var initial struct {
		Main struct {
			Entries []entry     `json:"entries"`
			Earlier *PageCursor `json:"earlier"`
		} `json:"main"`
	}
	if err := json.Unmarshal(snapshot.Data, &initial); err != nil {
		t.Fatal(err)
	}
	if len(initial.Main.Entries) == 0 {
		t.Fatal("latest window is empty")
	}
	latest := initial.Main.Entries[len(initial.Main.Entries)-1]
	if latest.Seq != 600 || latest.Content == nil || len(initial.Main.Entries) > pageEntries {
		t.Fatalf("latest window lost identity or reference: %+v", latest)
	}
	read, failure := s.content(testJSON(t, latest.Content))
	if failure != nil || read.(ContentResult).Entry.Text != publication.Entries[599].Text {
		t.Fatalf("complete content: %v", failure)
	}
	cursor := initial.Main.Earlier
	seen := len(initial.Main.Entries)
	for cursor != nil {
		read, failure := s.page(testJSON(t, cursor))
		if failure != nil {
			t.Fatal(failure)
		}
		page := read.(PageResult)
		data := testJSON(t, page)
		if len(data) > 32<<10 || len(page.Entries) == 0 || len(page.Entries) > pageEntries || page.Entries[len(page.Entries)-1].Seq != uint64(600-seen) {
			t.Fatalf("invalid bounded page after %d entries: %d bytes, %+v", seen, len(data), page)
		}
		seen += len(page.Entries)
		cursor = page.Earlier
	}
	if seen != 600 {
		t.Fatalf("retained %d of 600 entries", seen)
	}
	// Appending preserves history cursors; an earlier edit invalidates them.
	updated := &activity.Publication{Entries: append([]activity.Entry{}, publication.Entries...)}
	updated.Entries = append(updated.Entries, activity.Entry{Seq: 601, Text: "new", Revision: 1})
	frame.Main = updated
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if _, failure := s.page(testJSON(t, initial.Main.Earlier)); failure != nil {
		t.Fatal(failure)
	}
	changed := &activity.Publication{Entries: append([]activity.Entry{}, updated.Entries...)}
	changed.Entries[0].Revision++
	changed.Entries[0].Text = "changed historical evidence"
	frame.Main = changed
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if _, failure := s.page(testJSON(t, initial.Main.Earlier)); failure == nil || failure.Data.Status != 409 {
		t.Fatal("historical mutation did not invalidate cursor")
	}
	frame.Attachment++
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if _, failure := s.content(testJSON(t, latest.Content)); failure == nil || failure.Data.Status != 409 {
		t.Fatal("replaced attachment served stale content")
	}
}

func TestFeedPagesRetainDisplayOrderAndTurnNumbers(t *testing.T) {
	s := New()
	publication := &activity.Publication{}
	for i := range 100 {
		publication.Entries = append(publication.Entries, activity.Entry{Seq: uint64(100 - i), Agent: "You", Kind: "message", Text: "human turn", Revision: 1})
	}
	if err := s.Publish(Frame{Main: publication}); err != nil {
		t.Fatal(err)
	}
	latest, err := s.feedPage("main", &s.main, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	var first, last entry
	if err := json.Unmarshal(latest.Entries[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(latest.Entries[len(latest.Entries)-1], &last); err != nil {
		t.Fatal(err)
	}
	if first.Seq != 50 || last.Seq != 1 || first.Turn != 51 || last.Turn != 100 {
		t.Fatalf("display order or user turns changed: first=%+v last=%+v", first, last)
	}
	value, failure := s.page(testJSON(t, latest.Earlier))
	if failure != nil {
		t.Fatal(failure)
	}
	earlier := value.(PageResult).Entries
	if len(earlier) != 50 || earlier[0].Seq != 100 || earlier[49].Seq != 51 || earlier[0].Turn != 1 {
		t.Fatal("earlier page sorted identity instead of preserving display order")
	}
}

func TestCompactFeedPreservesPresentation(t *testing.T) {
	var edits []string
	for i := range 200 {
		edits = append(edits, fmt.Sprintf("Create `/tmp/tree/file%03d.go` +3 -0 · cp", i))
	}
	item := activity.ItemContent{Seq: 1, Agent: "Main", Kind: "tool", Text: strings.Join(edits, "\n\n")}
	command := "echo first\n" + strings.Repeat("echo next\n", 600)
	for _, test := range []struct {
		name  string
		entry activity.Entry
		check func(*testing.T, entry)
	}{
		{"edits", activity.Entry{Facts: item.Facts, Values: item.Values(), Revision: 1}, func(t *testing.T, e entry) {
			if e.Text != "" || e.AutoContent || len(e.Values) != 11 || e.Values[10].HiddenEdits != 190 || e.Values[0].Verb != "Create" || e.Values[0].Sources[0] != "cp" {
				t.Fatalf("compact edit presentation lost: %+v", e)
			}
		}},
		{"command", activity.Entry{Seq: 1, Kind: "tool", Agent: "Main", Text: "Run command", Values: []activity.Value{{Kind: "op", Verb: "Run", Code: command, Lang: "bash", Fenced: true, Tail: []string{strings.Repeat("output", 1000), "last line"}}}, Revision: 1}, func(t *testing.T, e entry) {
			if e.AutoContent || len(e.Values) != 1 || e.Values[0].Code != "echo first" || e.Values[0].CodeLines != 601 || e.Values[0].TailLines != 2 {
				t.Fatalf("command disclosures lost: %+v", e)
			}
		}},
		{"inline program", activity.Entry{Seq: 1, Kind: "tool", Agent: "Main", Text: "Run program", Values: []activity.Value{{Kind: "op", Verb: "Run", Label: "· via `python3`", Code: command, Lang: "python", Fenced: true}}, Revision: 1}, func(t *testing.T, e entry) {
			if e.AutoContent || len(e.Values) != 1 || e.Values[0].Label != "· via `python3`" || e.Values[0].Code != "echo first" || e.Values[0].CodeLines != 601 {
				t.Fatalf("program headline lost: %+v", e)
			}
		}},
		{"prose", activity.Entry{Seq: 1, Agent: "Main", Kind: "final", Text: strings.Repeat("**authored text**\n", 1000), Revision: 1}, func(t *testing.T, e entry) {
			if !e.AutoContent || len(e.Text) > 512 {
				t.Fatal("inline prose did not request automatic completion")
			}
		}},
		{"reply", activity.Entry{Seq: 1, Agent: "/root/child", Kind: "reply", Text: "retained reply", Values: []activity.Value{{Kind: "message", From: "/root/child", To: "/root", Body: strings.Repeat("Supporting text. ", 1000), ReplySummary: "Compact summary.", ReplyDetails: strings.Repeat("Supporting text. ", 1000)}}, Revision: 1}, func(t *testing.T, e entry) {
			if !e.AutoContent || e.Text != "" || e.Values[0].Summary != "Compact summary." || e.Values[0].Body == "" || len(e.Values[0].Body) > 512 || e.Values[0].Details != "" {
				t.Fatal("deferred reply lost its summary or semantic body")
			}
		}},
		{"long path", activity.Entry{Seq: 1, Agent: "Main", Kind: "tool", Text: "Read path", Values: []activity.Value{{Kind: "op", Verb: "Read", Reads: []activityfeed.Read{{Path: strings.Repeat("long/path/", 1000)}}}}, Revision: 1}, func(t *testing.T, e entry) {
			if !e.AutoContent {
				t.Fatal("long inline path was silently omitted")
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := New()
			if err := s.Publish(Frame{ChildReplySummary: true, Main: &activity.Publication{Entries: []activity.Entry{test.entry}}}); err != nil {
				t.Fatal(err)
			}
			page, err := s.feedPage("main", &s.main, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			var compact entry
			if len(page.Entries) != 1 || json.Unmarshal(page.Entries[0], &compact) != nil || compact.Content == nil {
				t.Fatal("missing compact entry")
			}
			test.check(t, compact)
			result, failure := s.content(testJSON(t, compact.Content))
			if failure != nil {
				t.Fatal(failure)
			}
			full := result.(ContentResult).Entry
			if string(testJSON(t, full)) != string(s.main.entries[1].data) {
				t.Fatal("content read lost original projection")
			}
		})
	}
}

func TestCompactFeedDefersManyImages(t *testing.T) {
	images := make([]string, 160)
	for i := range images {
		images[i] = fmt.Sprintf("/tmp/user-image-%d.png", i)
	}
	s := New()
	if err := s.Publish(Frame{Main: &activity.Publication{Entries: []activity.Entry{{Seq: 1, Agent: "You", Kind: "user", Text: "[images]", Images: images, Revision: 1}}}}); err != nil {
		t.Fatal(err)
	}
	page, err := s.feedPage("main", &s.main, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	var compact entry
	if len(page.Entries) != 1 || json.Unmarshal(page.Entries[0], &compact) != nil || !compact.AutoContent || compact.Content == nil {
		t.Fatal("images did not defer automatically")
	}
	result, failure := s.content(testJSON(t, compact.Content))
	if failure != nil || len(result.(ContentResult).Entry.Images) != len(images) {
		t.Fatal("complete content lost images", failure)
	}
}
