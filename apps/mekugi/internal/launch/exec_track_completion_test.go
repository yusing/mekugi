package launch

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
	before := u.attached.source.Model.Publish().Entries[0]
	rollCommandOutput(u)
	frame := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(100, 30).Lines, "\n"))
	if !strings.Contains(frame, "line 19") {
		t.Fatalf("tracked output did not advance:\n%s", frame)
	}
	if after := u.attached.source.Model.Publish().Entries[0]; !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal reveal revised command facts:\nbefore=%+v\nafter=%+v", before, after)
	}
	if !u.attached.source.MarkUnreturned("main", "cmd") {
		t.Fatal("running output presence depended on an animated model tail")
	}
	report.send(segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
	awaitMain(t, u, "Running echo last")
	frame = awaitMain(t, u, "output not returned to the model")
	after := u.attached.source.Model.Publish().Entries[0]
	if after.Seq != before.Seq || after.Revision == before.Revision || len(u.attached.source.Entries[0].Source.Native.Segments) != 2 || !strings.Contains(frame, "output not returned to the model") {
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
			seq := u.attached.source.Entries[0].Source.Seq
			target := activityui.Block{Source: seq, Output: 2}
			retained := u.attached.source.RetainedOutput(target.Source, target.Output)
			if retained == nil || strings.Join(retained.View().Lines, "\n") != "first" {
				t.Fatal("running segment output was not retained")
			}
			item["status"], item["exitCode"], item["aggregatedOutput"] = "failed", 2, "first\nlast\n"
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
			for _, view := range []*activityAdapter{u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents)} {
				entry := view.Model.Publish().Entries[0]
				if entry.Seq != seq || !entry.ItemComplete || entry.Running || !entry.OutputFailed {
					t.Fatalf("neutral completion waited for its report: %+v", entry)
				}
				if !strings.Contains(strings.Join(entry.Values[len(entry.Values)-1].Tail, "\n"), "last") {
					t.Fatalf("neutral consumer lost completed aggregate: %+v", entry.Values)
				}
			}
			if runningProcessCount(&u.attached.session) != 0 || u.attached.source.RetainedOutput(target.Source, target.Output) != retained {
				t.Fatal("report wait revived execution or retired the displayed segment target")
			}
			u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: u.attached.session.Activity().NextSequence(), Kind: "output_dedupe", CallID: "cmd", Text: "Repeated fixture output", Native: &sessionpresentation.NativeItem{Thread: "main", Item: "cmd"}}}})
			now = now.Add(execTrackCompletionWait - time.Nanosecond)
			u.flushTestStreamOutput(&u.attached, u.shell, u.clock, u.ctx)
			frame := ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(100, 30).Lines, "\n"))
			if u.attached.session.Commands()[key] == nil || !strings.Contains(frame, "Running") || strings.Contains(frame, "exit 2") || strings.Count(frame, "Repeated fixture output") != 1 {
				t.Fatalf("report wait lost its held frame or annotation:\n%s", frame)
			}
			if closeReport {
				report.send(
					segmenttransport.Message{Type: segmenttransport.Output, Index: 1, Data: "last\n"},
					segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(2)},
					segmenttransport.Message{Type: segmenttransport.Done, Code: new(2)},
				)
				// A segment's exit is visible before transport closure. Hold that
				// boundary explicitly so rendered output cannot stand in for settlement.
				awaitMain(t, u, "exit 2")
				entry := u.attached.source.Entries[0]
				if !entry.Source.Native.SegmentsPending || u.attached.session.Commands()[key] == nil || !hub.Tracking(key) {
					t.Fatal("segment exit settled the report before closure")
				}
				report.send(segmenttransport.Message{Type: segmenttransport.Closed})
				report.conn.Close()
				deadline := time.Now().Add(2 * time.Second)
				for u.attached.session.Commands()[key] != nil || hub.Tracking(key) {
					u.flushTestStreamOutput(&u.attached, u.shell, u.clock, u.ctx)
					if time.Now().After(deadline) {
						t.Fatalf("closed report did not retire: command=%t tracking=%t", u.attached.session.Commands()[key] != nil, hub.Tracking(key))
					}
					time.Sleep(5 * time.Millisecond)
				}
				frame = awaitMain(t, u, "exit 2")
			} else {
				now = now.Add(time.Nanosecond)
				u.flushTestStreamOutput(&u.attached, u.shell, u.clock, u.ctx)
				frame = ansi.Strip(strings.Join(u.testActivity(u.shell.Transcript).RenderFeed(100, 30).Lines, "\n"))
			}
			entry := u.attached.source.Entries[0]
			if entry.Source.Seq != seq || entry.Source.Native.SegmentsPending || (len(entry.Source.Native.Segments) == 2) != closeReport || u.attached.session.Commands()[key] != nil || hub.Tracking(key) {
				t.Fatalf("report did not settle on its original identity: seq=%d want=%d pending=%t segments=%d wantSeparate=%t command=%t tracking=%t", entry.Source.Seq, seq, entry.Source.Native.SegmentsPending, len(entry.Source.Native.Segments), closeReport, u.attached.session.Commands()[key] != nil, hub.Tracking(key))
			}
			if !strings.Contains(frame, "exit 2") || !strings.Contains(frame, "last") || strings.Count(frame, "Repeated fixture output") != 1 {
				t.Fatalf("settlement lost outcome, output or annotation:\n%s", frame)
			}
		})
	}
}
