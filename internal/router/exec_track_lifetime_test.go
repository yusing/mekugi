package router

import (
	"slices"
	"testing"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestExecTrackSessionReplacementDropsRetainedChildOutput(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
	key := [3]string{"child", "turn", "command"}
	script := "echo first; echo second"
	item := map[string]any{"id": key[2], "type": "commandExecution", "command": "bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	claimed, track, ok := hub.claim(t.Context(), segmenttransport.Message{Type: segmenttransport.Hello, Thread: key[0], Script: script, Segments: []string{"echo first", "echo second"}})
	if !ok || claimed != key {
		t.Fatal("child command report was not claimed")
	}
	report := execTrackStream{hub: hub.execTrackHub, key: key, track: track}
	report.Observe(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0})
	report.Observe(segmenttransport.Message{Type: segmenttransport.Output, Index: 0, Data: "first\n"})
	u.flushStreamOutput()
	before, _ := hub.view(key, false, execSegmentText, u.session.activity.Outputs(&u.session.state))
	if len(before.segments) != 1 || before.segments[0].output == nil || !slices.Equal(before.segments[0].output.View().Lines, []string{"first"}) {
		t.Fatalf("child output was not retained before replacement: %+v", before)
	}
	retired := before.segments[0].output
	if err := u.clearSessionPresentation(); err != nil {
		t.Fatal(err)
	}
	if !retired.View().Released {
		t.Error("detached child output remains retained")
	}
	if !hub.tracking(key) {
		t.Fatal("presentation replacement canceled host reporting")
	}
	// A subsequent attachment may see the same still-running command. Losing
	// its earlier bytes must not turn the remaining bytes into complete output.
	u.session.start("other", u.session.cwd)
	report.Observe(segmenttransport.Message{Type: segmenttransport.Output, Index: 0, Data: "later\n"})
	report.Observe(segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)})
	report.Observe(segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
	report.Observe(segmenttransport.Message{Type: segmenttransport.Output, Index: 1, Data: "second\n"})
	report.Observe(segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(3)})
	report.Observe(segmenttransport.Message{Type: segmenttransport.Done, Code: new(3)})
	report.Observe(segmenttransport.Message{Type: segmenttransport.Closed})
	report.End()
	after, _ := hub.view(key, true, execSegmentText, u.session.activity.Outputs(&u.session.state))
	if after.output || !after.complete || after.code != 3 || len(after.segments) != 2 {
		t.Fatalf("detached report lost outcomes or claimed complete output: %+v", after)
	}
	for _, segment := range after.segments {
		if segment.output != nil {
			t.Error("detached segment acquired retention in the new attachment")
		}
	}
	// Host completion still uses its authoritative combined output.
	entry := activityPaneEntry{native: &liveActivityNativeItem{thread: key[0], turn: key[1], item: key[2]}}
	done := u.trackedCommandDone(key, entry, appServerItem{Type: "commandExecution", ExitCode: new(3), AggregatedOutput: new("first\nlater\nsecond\n")})
	if len(done) != 1 || !slices.Equal(done[0].outputTail, []string{"first", "later", "second"}) {
		t.Fatalf("combined host output was lost: %+v", done)
	}
}
