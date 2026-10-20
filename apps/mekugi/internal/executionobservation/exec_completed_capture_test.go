package executionobservation

import (
	"context"

	"github.com/yusing/mekugi/modules/commandsegments/segments"

	"path/filepath"

	"slices"

	"strings"

	"testing"

	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestExecCompletedCaptureOwnershipRetryAndWithdrawal(t *testing.T) {
	for _, scenario := range []string{"parallel writer", "snapshot retry", "return to baseline", "unrelated overlap", "sequential hosts"} {
		t.Run(scenario, func(t *testing.T) {
			hub := newExecTrackFixture(t, t.Context(), segments.Instrumentation{})
			workspace := t.TempDir()
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
				hub.start([3]string{"main", "turn", call}, "/bin/bash -lc "+quoteShellWord(command.Command))
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
					hub.await(t, [3]string{"main", "turn", call}, func(view segments.Report) bool { return (view.LastStarted() + 1) == completed+1 })
				} else if scenario != "sequential hosts" {
					report.send(segmenttransport.Message{Type: segmenttransport.Begin})
					hub.await(t, [3]string{"main", "turn", call}, func(view segments.Report) bool { return (view.LastStarted() + 1) == 1 })
				}
			}
			budget := changecapture.MaxContentBytes
			observation := changecapture.Observation{Commands: commands, Files: []changecapture.File{changecapture.ReadFile(path, &budget)}, Labels: []string{"python3"}, RepeatedPaths: true}
			registry := &Windows{}
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
				hub.await(t, [3]string{"main", "turn", "sibling"}, func(view segments.Report) bool { return (view.LastStarted()+1) == 1 && view.Segments[0].Ended })
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
			delivered := false
			for _, event := range testReview(broker).TakePreviews(sub) {
				if event.Preview != nil && event.Preview.EditReceipt == capture.text && event.Preview.Complete {
					delivered = true
				}
			}
			if !delivered {
				t.Fatal("completed receipt was not published")
			}

			if scenario == "sequential hosts" {
				previous := capture.completed
				hub.Completed([3]string{"main", "turn", "edit"})
				reports[1].send(segmenttransport.Message{Type: segmenttransport.Begin})
				reports[1].send(segmenttransport.Message{Type: segmenttransport.End, Code: new(0)})
				hub.await(t, [3]string{"main", "turn", "sibling"}, func(view segments.Report) bool { return (view.LastStarted()+1) == 1 && view.Segments[0].Ended })
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
				hub.await(t, [3]string{"main", "turn", "edit"}, func(view segments.Report) bool { return (view.LastStarted()+1) == 2 && view.Segments[1].Ended })
				capture.next = time.Time{}
				capture.update(t.Context(), broker, preview, tracking, tracking.state())
				withdrawn := false
				for _, event := range testReview(broker).TakePreviews(sub) {
					if event.Preview != nil && event.Preview.EditReceipt == "" && event.Preview.ID == "observed:cell" {
						withdrawn = true
					}
				}
				if capture.text != "" || !withdrawn {
					t.Fatal("endpoint retained reverted counts or lost withdrawal")
				}

			}
		})
	}
}
