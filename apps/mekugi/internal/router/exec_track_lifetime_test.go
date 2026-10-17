package router

import (
	"slices"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestExecTrackSessionReplacementDropsRetainedChildOutput(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	u.session.registerThread(binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
	key := [3]string{"child", "turn", "command"}
	script := "echo first; echo second"
	item := map[string]any{"id": key[2], "type": "commandExecution", "command": "bash -lc " + quoteShellWord(script), "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	report := dialExecTrackReportThread(t, hub, key[0], script)
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.Output, Index: 0, Data: "first\n"})
	hub.await(t, key, func(view execTrackView) bool { return len(view.segments) == 1 && len(view.segments[0].Tail) != 0 })
	u.flushStreamOutput()
	before, _ := hub.view(key, false, execSegmentText)
	if len(before.segments) != 1 || before.segments[0].Output == nil || !slices.Equal(before.segments[0].Output.View().Lines, []string{"first"}) {
		t.Fatalf("child output was not retained before replacement: %+v", before)
	}
	retired := before.segments[0].Output
	if err := u.clearSessionPresentation(); err != nil {
		t.Fatal(err)
	}
	if !retired.View().Released {
		t.Error("detached child output remains retained")
	}
	if !hub.Tracking(key) {
		t.Fatal("presentation replacement canceled host reporting")
	}
	// A subsequent attachment may see the same still-running command. Losing
	// its earlier bytes must not turn the remaining bytes into complete output.
	u.releaseRetired("main")
	u.session.start("main", u.session.cwd)
	u.session.registerThread(binding.ThreadInfo{ID: "child", AgentNickname: "worker"})
	report.send(segmenttransport.Message{Type: segmenttransport.Output, Index: 0, Data: "later\n"})
	report.send(segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)})
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 1})
	report.send(segmenttransport.Message{Type: segmenttransport.Output, Index: 1, Data: "second\n"})
	report.send(segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(3)})
	report.send(segmenttransport.Message{Type: segmenttransport.Done, Code: new(3)})
	report.send(segmenttransport.Message{Type: segmenttransport.Closed})
	hub.await(t, key, func(view execTrackView) bool { return view.complete })
	after, _ := hub.view(key, true, execSegmentText)
	if after.output || !after.complete || after.code != 3 || len(after.segments) != 2 {
		t.Fatalf("detached report lost outcomes or claimed complete output: %+v", after)
	}
	for _, segment := range after.segments {
		if segment.Output != nil {
			t.Error("detached segment acquired retention in the new attachment")
		}
	}
	// Without a command in the new attachment, the actual host completion
	// retires tracking and presents its authoritative combined output.
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 3, "first\nlater\nsecond\n"
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	if hub.Tracking(key) || u.session.commands[key] != nil {
		t.Fatal("host completion retained the old command observation")
	}
	for _, entry := range u.agents.Entries {
		native := entry.Source.Native
		if native == nil || native.Thread != key[0] || native.Item != key[2] {
			continue
		}
		if native.Running || len(native.Segments) != 0 || native.Output == nil {
			t.Fatalf("completion did not use combined host output: %+v", native)
		}
		output := native.Output.View()
		if !output.Done || output.Exit != 3 || !slices.Equal(output.Lines, []string{"first", "later", "second"}) {
			t.Fatalf("combined host output was lost: %+v", output)
		}
		return
	}
	t.Fatal("host completion did not reach Activity")
}
