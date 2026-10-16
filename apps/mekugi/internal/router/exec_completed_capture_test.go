package router

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestExecCompletedCaptureOwnershipRetryAndWithdrawal(t *testing.T) {
	for _, scenario := range []string{"parallel writer", "snapshot retry", "return to baseline", "unrelated overlap", "sequential hosts"} {
		t.Run(scenario, func(t *testing.T) {
			u, hub := newTrackedAppServerUI(t)
			workspace := u.session.cwd
			path := filepath.Join(workspace, "a.go")
			writeTestFile(t, path, "before\n")
			commands := []commandanalysis.Command{{Command: "python3 z.py; read -r answer", Workdir: workspace}}
			if scenario == "sequential hosts" {
				commands[0].Command = "printf ready; printf again; python3 z.py; read -r answer"
			}
			if scenario == "parallel writer" || scenario == "sequential hosts" {
				commands = append(commands, commandanalysis.Command{Command: "python3 a.py", Workdir: workspace})
			}
			tracking := newExecPreviewTrack(hub.Observer, "main", "turn", commands)
			defer tracking.Close()
			var reports []*execTrackReport
			for i, command := range commands {
				call := "edit"
				if i > 0 {
					call = "sibling"
				}
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": appServerItem{ID: call, Type: "commandExecution", Command: "/bin/bash -lc " + quoteShellWord(command.Command), Status: "inProgress"}})
				report := dialExecTrackReport(t, hub, command.Command)
				reports = append(reports, report)
				if i == 0 {
					completed := 1
					if scenario == "sequential hosts" {
						completed = 3
					}
					for index := range completed {
						report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: index}, segmenttransport.Message{Type: segmenttransport.End, Index: index, Code: new(0)})
					}
					report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: completed})
					hub.await(t, [3]string{"main", "turn", call}, func(view execTrackView) bool { return len(view.segments) == completed+1 })
				} else if scenario != "sequential hosts" {
					report.send(segmenttransport.Message{Type: segmenttransport.Begin})
					hub.await(t, [3]string{"main", "turn", call}, func(view execTrackView) bool { return len(view.segments) == 1 })
				}
			}
			budget := changecapture.MaxContentBytes
			observation := changecapture.Observation{Commands: commands, Files: []changecapture.File{changecapture.ReadFile(path, &budget)}, Labels: []string{"python3"}, RepeatedPaths: true}
			registry := &execWindowRegistry{}
			registry.open(&execWindow{ref: "cell", thread: "main", turn: "turn", roots: []string{workspace}})
			defer registry.close("cell")
			broker := newLivePreview(t.Context())
			setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
			sub := testReview(broker).Subscribe()
			capture := &execPreviewCapture{registry: registry, ref: "cell", observation: observation}
			preview := previewmodel.Preview{Workspace: workspace, Thread: "main", Turn: "turn"}
			if scenario == "snapshot retry" || scenario == "unrelated overlap" {
				capture.snapshots = testCaptureWorkspaces(t)
				defer capture.snapshots.Close()
				capture.observation.Tree = capture.snapshots.Checkpoint(t.Context(), workspace)
				if capture.observation.Tree == "" {
					t.Fatal("missing checkpoint")
				}
				if scenario == "snapshot retry" {
					capture.observation.Files = nil // All targets are computed by the program.
				} else {
					registry.open(&execWindow{ref: "other", thread: "other", roots: []string{workspace}, paths: []string{filepath.Join(workspace, "other.go")}})
					defer registry.close("other")
				}
			}
			writeTestFile(t, path, "after\n")
			if scenario == "parallel writer" {
				capture.update(t.Context(), broker, preview, tracking, tracking.state())
				if capture.text != "" {
					t.Fatal("running sibling supplied a completed receipt")
				}
				reports[1].send(segmenttransport.Message{Type: segmenttransport.End, Code: new(0)})
				hub.await(t, [3]string{"main", "turn", "sibling"}, func(view execTrackView) bool { return len(view.segments) == 1 && !view.segments[0].running })
			}
			if scenario == "snapshot retry" {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				capture.update(ctx, broker, preview, tracking, tracking.state())

				if capture.completed != 0 {
					t.Fatal("failed comparison consumed its boundary")
				}
				if capture.snapshots.Checkpoint(t.Context(), workspace) == "" {
					t.Fatal("snapshot failed to recover")
				}
			}
			capture.next = time.Time{}
			capture.update(t.Context(), broker, preview, tracking, tracking.state())
			if !strings.Contains(capture.text, "+1 -1") {
				t.Fatal("completed edit lacks counts", capture.text)
			}
			for _, event := range testReview(broker).TakePreviews(sub) {
				if event.Preview != nil {
					u.shell.preview(*event.Preview, false)
				}
			}
			awaitMain(t, u, "Edited")
			if scenario == "sequential hosts" {
				previous := capture.completed
				hub.Completed([3]string{"main", "turn", "edit"})
				reports[1].send(segmenttransport.Message{Type: segmenttransport.Begin})
				reports[1].send(segmenttransport.Message{Type: segmenttransport.End, Code: new(0)})
				hub.await(t, [3]string{"main", "turn", "sibling"}, func(view execTrackView) bool { return len(view.segments) == 1 && !view.segments[0].running })
				writeTestFile(t, path, "second\nline\n")
				capture.next = time.Time{}
				capture.update(t.Context(), broker, preview, tracking, tracking.state())
				if capture.completed <= previous || !strings.Contains(capture.text, "+2 -1") || !slices.Equal(capture.calls, []string{"edit", "sibling"}) {
					t.Fatalf("later receipt lost counts or host order: %+v", capture)
				}
			}
			if scenario == "return to baseline" {
				writeTestFile(t, path, "before\n")
				reports[0].send(segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(0)})
				hub.await(t, [3]string{"main", "turn", "edit"}, func(view execTrackView) bool { return len(view.segments) == 2 && !view.segments[1].running })
				capture.next = time.Time{}
				capture.update(t.Context(), broker, preview, tracking, tracking.state())
				for _, event := range testReview(broker).TakePreviews(sub) {
					if event.Preview != nil {
						u.shell.preview(*event.Preview, false)
					}
				}
				if capture.text != "" {
					t.Fatal("endpoint retained reverted counts")
				}
				for _, view := range []*activityAdapter{u.view, u.agents} {
					for _, entry := range view.Entries {
						if entry.Source.native != nil && entry.Source.native.capturedEdit != nil {
							t.Fatal("UI retained a withdrawn receipt")
						}
					}
				}
			}
		})
	}
}

func TestExecCompletedReceiptKeepsPythonValidation(t *testing.T) {
	writer, check := "cat > source.go", "python3 -c 'assert True'"
	command := writer + "; " + check
	entry := activityPaneEntry{Kind: "tool", Text: toolActivityShell(command), native: &liveActivityNativeItem{
		thread: "main", item: "cmd", command: command,
		segments:     []commandSegment{{source: writer, text: execSegmentText(writer)}, {source: check, text: execSegmentText(check)}},
		capturedEdit: &capturedActivityEdit{thread: "main", calls: []string{"cmd"}, text: "Edit `source.go` +1 -1 · cat"},
	}}
	blocks := commandSegmentBlocks(commandPresentation(entry))
	if !slices.ContainsFunc(blocks, func(block activityui.Block) bool {
		return block.Code == check || strings.Contains(block.Label, "assert True")
	}) {
		t.Fatalf("receipt removed the Python validation: %+v", blocks)
	}
}
