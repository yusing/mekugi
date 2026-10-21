package sessionpresentation

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/transcript"
)

func TestObservationReductionWithoutTerminal(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			var attachment Attachment
			attachment.Start("main")
			attachment.Activity().StartRoster(ActivityAgent{Thread: "main", Name: "/root"})
			feeds := attachment.Activity()
			main := &ActivitySource{Model: &feeds.Main, Audience: MainAudience}
			child := &ActivitySource{Model: &feeds.Children, Audience: ChildAudience}
			model := new(Model)
			model.Attach(Thread{ID: "main", Caller: "/root"}, false)
			var text transcript.State
			var waits activity.Waits
			var outputs feed.Retention
			at := time.Unix(100, 0)
			reduce := func(kind observation.Kind, item observation.Item, delta string) ObservationResult {
				in := ObservationInput{Main: "main", Workspace: "/workspace", Event: observation.Event{Kind: kind, ThreadID: thread, TurnID: "turn", ItemID: item.ID, Item: item, Delta: delta, At: at}}
				in.Text = observation.TextEvent{Kind: kind, Delta: delta, At: at, Item: observation.TextItem{Ref: observation.ItemRef{ThreadID: thread, TurnID: "turn", ItemID: item.ID}, Type: item.Type, Summary: item.Summary}}
				return ReduceObservation(&attachment, model, main, child, &text, &waits, &outputs, in)
			}
			item := observation.Item{ID: "cmd", Type: "commandExecution", Command: "go test ./...", Status: "inProgress"}
			started := reduce(observation.ItemStarted, item, "")
			if len(started.Deliveries) != 1 || started.Deliveries[0].Command == nil {
				t.Fatalf("missing detached start: %+v", started)
			}
			key := [3]string{thread, "turn", item.ID}
			invocation := attachment.Commands()[key].Entry.Native.Invocation
			body := strings.Repeat("observed output\n", 20)
			reduce(observation.Kind("item/commandExecution/outputDelta"), item, body)
			item.Status, item.ExitCode, item.AggregatedOutput = "completed", new(0), &body
			at = at.Add(time.Second)
			completed := reduce(observation.ItemCompleted, item, "")
			if attachment.Commands()[key] != nil || invocation.Running() || len(completed.Deliveries) != 1 {
				t.Fatal("source completion depends on a terminal consumer")
			}
			source := main
			if thread == "child" {
				source = child
			}
			entry := source.Entries[0].Source
			if entry.Native.Invocation != invocation || entry.Native.Phase != "item/completed" || len(entry.OutputTail) != 5 {
				t.Fatalf("completion lost source identity or output: %+v", entry)
			}
			item.Status = "inProgress"
			reduce(observation.ItemStarted, item, "")
			reduce(observation.Kind("item/commandExecution/outputDelta"), item, "late output")
			if attachment.Commands()[key] != nil || invocation.Running() || source.Entries[0].Source.Native.Invocation != invocation {
				t.Fatal("late input reopened completed source state")
			}
			reasoning := observation.Item{ID: "reasoning", Type: "reasoning", Summary: []string{"Complete public summary"}}
			reduce(observation.ItemCompleted, reasoning, "")
			if last := source.Entries[len(source.Entries)-1].Source; last.Text != reasoning.Summary[0] || last.Native.Phase != "item/completed" {
				t.Fatalf("reasoning admission requires a renderer: %+v", last)
			}
			before := source.Revision
			attachment.Retire()
			if result := reduce(observation.ItemStarted, item, ""); result.Publish || source.Revision != before {
				t.Fatal("retired attachment accepted an observation")
			}
		})
	}
}

func TestCommandReportReductionWithoutReveal(t *testing.T) {
	invocation := activity.NewInvocation(activity.CommandSource{}, time.Time{}, nil)
	output := strings.Repeat("amber1 +1 -0\n", 10)
	state := ObserveCommandReport(invocation, activity.SegmentReport{
		Ended: true, Closed: true, Done: true,
		Segments: []activity.SegmentObservation{
			{Source: "mchanges --list", Text: "List changes", Began: true, Finished: true, Output: output},
			{Source: "printf done"},
		},
	}, true)
	if !state.Snapshot.Complete || len(state.Segments) != 2 || !state.Segments[1].Skipped {
		t.Fatalf("source report did not settle: %+v", state)
	}
	segment := state.Segments[0]
	if len(segment.Tail) != 5 || segment.Omit != 5 || segment.Output != nil || len(segment.Changes) != 6 || segment.Changes[0].OmittedLines != 5 {
		t.Fatalf("bounded source summary requires retention or terminal reveal: %+v", segment)
	}
}

func TestCommandReportResultCounts(t *testing.T) {
	for _, lossy := range []bool{false, true} {
		invocation := activity.NewInvocation(activity.CommandSource{}, time.Time{}, new(feed.Retention))
		state := ObserveCommandReport(invocation, activity.SegmentReport{
			Ended: true, Closed: true, Done: true, Lossy: lossy,
			Segments: []activity.SegmentObservation{
				{Source: "rg 'x|^$' matches.txt", Analysis: "rg 'x|^$' matches.txt", Text: "Search `x|^$` in `matches.txt`", Began: true, Finished: true, Output: "x\n\n"},
				{Source: "inspect_file a.go", Analysis: "inspect_file a.go", Text: "Inspect `a.go`", Began: true, Finished: true, Output: "1-2 import\n4-6 function main\n"},
			},
		}, true)
		// Repeated publication reads the same retained rows.
		if again := invocation.ReportSnapshot(true); !lossy && (again.Segments[0].Results == nil || *again.Segments[0].Results != 2) {
			t.Fatal("result count changed after publication")
		}
		entry := ActivityEntry{Kind: "tool", Native: &NativeItem{Segments: state.Segments}}
		command := CommandPresentation(entry)
		values := command.SegmentValues(false)
		if len(values) != 2 {
			t.Fatalf("values: %+v", values)
		}
		for _, value := range values {
			if lossy {
				if value.Results != nil {
					t.Fatal("count from lost attribution")
				}
			} else if value.Results == nil || *value.Results != 2 {
				t.Fatalf("missing segment count: %+v", value)
			}
		}
	}
}

func TestCommandReportCountLimitsPreserveAttribution(t *testing.T) {
	invocation := activity.NewInvocation(activity.CommandSource{}, time.Time{}, new(feed.Retention))
	report := activity.SegmentReport{Segments: []activity.SegmentObservation{
		{Source: "rg --files", Analysis: "rg --files", Text: "List `.`", Began: true, Output: strings.Repeat("a.go\n", 120000)},
		{Source: "ls src", Analysis: "ls src", Text: "List `src`"},
	}}
	ObserveCommandReport(invocation, report, false)
	report.Ended, report.Closed, report.Done = true, true, true
	report.Segments[0].Finished = true
	report.Segments[1].Began, report.Segments[1].Finished, report.Segments[1].Output = true, true, "one\ntwo\n"
	state := ObserveCommandReport(invocation, report, true)
	if state.Release || !state.Snapshot.SeparateOutput || state.Segments[0].Output == nil || state.Segments[1].Output == nil {
		t.Fatalf("count limit lost segment attribution: %+v", state)
	}
	values := CommandPresentation(ActivityEntry{Kind: "tool", Native: &NativeItem{Segments: state.Segments}}).SegmentValues(false)
	if len(values) != 2 || values[0].Results != nil || values[1].Results == nil || *values[1].Results != 2 {
		t.Fatalf("count limit affected sibling results: %+v", values)
	}
}
