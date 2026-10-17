package router

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestTrackedRevealDoesNotReviseCommandFacts(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	script := "echo first; echo last"
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, script)
	var output strings.Builder
	for i := range 20 {
		fmt.Fprintf(&output, "line %d\n", i)
	}
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.Output, Index: 0, Data: output.String()})
	awaitMain(t, u, "line 4")
	before := u.source.Model.Publish().Entries[0]
	rollCommandOutput(u)
	frame := ansi.Strip(strings.Join(u.testActivity(u.view).RenderFeed(100, 30).Lines, "\n"))
	if !strings.Contains(frame, "line 19") {
		t.Fatalf("tracked output did not advance:\n%s", frame)
	}
	if after := u.source.Model.Publish().Entries[0]; !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal reveal revised command facts:\nbefore=%+v\nafter=%+v", before, after)
	}
	if !u.source.MarkUnreturned("main", "cmd") {
		t.Fatal("running output presence depended on an animated model tail")
	}
	report.send(segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
	awaitMain(t, u, "Running echo last")
	frame = awaitMain(t, u, "output not returned to the model")
	after := u.source.Model.Publish().Entries[0]
	if after.Seq != before.Seq || after.Revision == before.Revision || len(u.source.Entries[0].Source.Native.Segments) != 2 || !strings.Contains(frame, "output not returned to the model") {
		t.Fatalf("segment lifecycle or output annotation was lost:\n%s", frame)
	}
}

func TestTrackedCompletionPrecedesReportSettlement(t *testing.T) {
	for _, closeReport := range []bool{true, false} {
		t.Run(map[bool]string{true: "closure", false: "deadline"}[closeReport], func(t *testing.T) {
			u, hub := newTrackedAppServerUI(t)
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			u.clock = func() time.Time { return now }
			key := [3]string{"main", "t", "cmd"}
			script := "echo first; echo last"
			item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "bash -lc " + quoteShellWord(script), "status": "inProgress"}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
			report := dialExecTrackReport(t, hub, script)
			report.send(
				segmenttransport.Message{Type: segmenttransport.Begin, Index: 0},
				segmenttransport.Message{Type: segmenttransport.Output, Index: 0, Data: "first\n"},
				segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)},
				segmenttransport.Message{Type: segmenttransport.Begin, Index: 1},
			)
			awaitMain(t, u, "┆ first")
			awaitMain(t, u, "Running echo last")
			seq := u.source.Entries[0].Source.Seq
			target := activityui.Block{Source: seq, Output: 2}
			retained := u.source.RetainedOutput(target.Source, target.Output)
			if retained == nil || strings.Join(retained.View().Lines, "\n") != "first" {
				t.Fatal("running segment output was not retained")
			}
			item["status"], item["exitCode"], item["aggregatedOutput"] = "failed", 2, "first\nlast\n"
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
			for _, view := range []*activityAdapter{u.testActivity(u.view), u.testActivity(u.agents)} {
				entry := view.Model.Publish().Entries[0]
				if entry.Seq != seq || !entry.ItemComplete || entry.Running || !entry.OutputFailed {
					t.Fatalf("neutral completion waited for its report: %+v", entry)
				}
				if !strings.Contains(strings.Join(entry.Values[len(entry.Values)-1].Tail, "\n"), "last") {
					t.Fatalf("neutral consumer lost completed aggregate: %+v", entry.Values)
				}
			}
			if u.runningProcessCount() != 0 || u.source.RetainedOutput(target.Source, target.Output) != retained {
				t.Fatal("report wait revived execution or retired the displayed segment target")
			}
			u.testActivity(u.view).Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: u.session.Activity().NextSequence(), Kind: "output_dedupe", CallID: "cmd", Text: "Repeated fixture output", Native: &sessionpresentation.NativeItem{Thread: "main", Item: "cmd"}}}})
			now = now.Add(execTrackCompletionWait - time.Nanosecond)
			u.flushStreamOutput()
			frame := ansi.Strip(strings.Join(u.testActivity(u.view).RenderFeed(100, 30).Lines, "\n"))
			if u.session.Commands()[key] == nil || !strings.Contains(frame, "Running") || strings.Contains(frame, "exit 2") || strings.Count(frame, "Repeated fixture output") != 1 {
				t.Fatalf("report wait lost its held frame or annotation:\n%s", frame)
			}
			if closeReport {
				report.send(
					segmenttransport.Message{Type: segmenttransport.Output, Index: 1, Data: "last\n"},
					segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(2)},
					segmenttransport.Message{Type: segmenttransport.Done, Code: new(2)},
					segmenttransport.Message{Type: segmenttransport.Closed},
				)
				report.conn.Close()
				frame = awaitMain(t, u, "exit 2")
			} else {
				now = now.Add(time.Nanosecond)
				u.flushStreamOutput()
				frame = ansi.Strip(strings.Join(u.testActivity(u.view).RenderFeed(100, 30).Lines, "\n"))
			}
			entry := u.source.Entries[0]
			if entry.Source.Seq != seq || entry.Source.Native.SegmentsPending || (len(entry.Source.Native.Segments) == 2) != closeReport || u.session.Commands()[key] != nil || hub.Tracking(key) {
				t.Fatalf("report did not settle on its original identity: %+v", entry.Source)
			}
			if !strings.Contains(frame, "exit 2") || !strings.Contains(frame, "last") || strings.Count(frame, "Repeated fixture output") != 1 {
				t.Fatalf("settlement lost outcome, output or annotation:\n%s", frame)
			}
		})
	}
}
