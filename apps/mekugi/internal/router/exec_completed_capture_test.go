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
			tracking := newExecPreviewTrack(hub.execTrackHub, "main", "turn", commands)
			defer tracking.close()
			for i, command := range commands {
				call := "edit"
				if i > 0 {
					call = "sibling"
				}
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": appServerItem{ID: call, Type: "commandExecution", Command: "/bin/bash -lc " + quoteShellWord(command.Command), Status: "inProgress"}})
				match := tracking.matches[command.Command]
				match.track.hostOnly = false
				match.track.segments = []execTrackSegment{{source: "python3 opaque.py", edit: execSegmentEdits("python3 opaque.py"), began: true, ended: i == 0}}
			}
			if scenario == "sequential hosts" {
				tracking.matches[commands[0].Command].track.segments = []execTrackSegment{{began: true, ended: true}, {began: true, ended: true}, {began: true, ended: true}, {began: true}}
				tracking.matches[commands[1].Command].track.segments[0].began = false
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
				tracking.matches[commands[1].Command].track.segments[0].ended = true
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
				hub.completed([3]string{"main", "turn", "edit"})
				tracking.matches[commands[1].Command].track.segments[0].began = true
				tracking.matches[commands[1].Command].track.segments[0].ended = true
				writeTestFile(t, path, "second\nline\n")
				capture.next = time.Time{}
				capture.update(t.Context(), broker, preview, tracking, tracking.state())
				if capture.completed <= previous || !strings.Contains(capture.text, "+2 -1") || !slices.Equal(capture.calls, []string{"edit", "sibling"}) {
					t.Fatalf("later receipt lost counts or host order: %+v", capture)
				}
			}
			if scenario == "return to baseline" {
				writeTestFile(t, path, "before\n")
				tracking.matches[commands[0].Command].track.segments = append(tracking.matches[commands[0].Command].track.segments, execTrackSegment{edit: true, began: true, ended: true})
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
				for _, view := range []*liveActivityView{u.view, u.agents} {
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
	blocks := commandSegmentBlocks(entry, false)
	if !slices.ContainsFunc(blocks, func(block activityui.Block) bool {
		return block.Code == check || strings.Contains(block.Label, "assert True")
	}) {
		t.Fatalf("receipt removed the Python validation: %+v", blocks)
	}
}
