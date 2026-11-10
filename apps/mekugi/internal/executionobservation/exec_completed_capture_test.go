package executionobservation

import (
	"context"

	"github.com/yusing/mekugi/contracts/activitydetail"

	"github.com/yusing/mekugi/modules/commandsegments/segments"

	"path/filepath"

	"slices"

	"strings"

	"testing"

	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/legacycall"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestExecCompletedCaptureOwnershipRetryAndWithdrawal(t *testing.T) {
	for _, scenario := range []string{"parallel writer", "snapshot retry", "return to baseline", "unrelated overlap", "sequential hosts", "temporary cat"} {
		t.Run(scenario, func(t *testing.T) {
			hub := newExecTrackFixture(t, t.Context(), segments.Instrumentation{})
			workspace := t.TempDir()
			path := filepath.Join(workspace, "a.go")
			writeTestFile(t, path, "before\n")
			commands := []commandanalysis.Command{{Command: "python3 z.py; read -r answer", Workdir: workspace}}
			if scenario == "temporary cat" {
				commands[0].Command = "cat >\"$fixture_dir/main.js\" <<'EOF'\nafter\nEOF\nread -r answer"
				path = filepath.Join(t.TempDir(), "main.js")
			}
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
			registry.find("cell").tracking = tracking
			defer registry.close("cell")
			broker := newLivePreview(t.Context())
			setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
			sub := testReview(broker).Subscribe()
			capture := &execPreviewCapture{registry: registry, ref: "cell", observation: observation}
			if scenario == "temporary cat" {
				capture.observation.Files, capture.observation.Labels = nil, []string{"cat"}
				reports[0].send(segmenttransport.Message{Type: segmenttransport.Created, Data: filepath.Dir(path), Tree: true})
				hub.await(t, [3]string{"main", "turn", "edit"}, func(view segments.Report) bool { return len(view.Created) == 1 })
			}
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
			counts := "+1 -1"
			if scenario == "temporary cat" {
				counts = "+1 -0"
			}
			if !strings.Contains(capture.text, counts) {
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
				capture.update(t.Context(), broker, preview, tracking, tracking.state())
				if capture.completed <= previous || !strings.Contains(capture.text, "+2 -1") || !slices.Equal(capture.calls, []string{"edit", "sibling"}) {
					t.Fatalf("later receipt lost counts or host order: %+v", capture)
				}
			}
			if scenario == "return to baseline" {
				writeTestFile(t, path, "before\n")
				reports[0].send(segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(0)})
				hub.await(t, [3]string{"main", "turn", "edit"}, func(view segments.Report) bool { return (view.LastStarted()+1) == 2 && view.Segments[1].Ended })
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

// Drive segment reports through capture publication, then cold retained hydration.
// The combined Python/formatter effect belongs before the unrelated test, even
// while that test has not started and when its eventual result fails.
func TestExecReceiptWriterBoundaryLiveAndRetained(t *testing.T) {
	f := newMChangesSliceFixture(t, "writer-anchor")
	proxy := testProxyWithStore(f.store, &Windows{})
	hub := newExecTrackFixture(t, t.Context(), segments.Instrumentation{})
	proxy.Windows.Reports = hub.Observer
	t.Cleanup(proxy.Windows.Retire)
	path := filepath.Join(f.workspace, "a.go")
	writeTestFile(t, path, "before\n")
	script := "python3 - <<'PY'\np='a.go'\ns=open(p).read().replace('before','after')\nopen(p,'w').write(s)\nPY\ngofmt -w a.go\ngo test ./..."
	commands := []commandanalysis.Command{{Command: script, Workdir: f.workspace, Shell: "bash"}}
	observed, ok := changecapture.Capture(f.ctx, commands, false, false, changecapture.Options{Directory: f.workspace})
	if !ok || observed == nil {
		t.Fatal("writer scope missing")
	}
	proxy.Windows.open(&execWindow{ref: "writer", thread: f.thread, turn: "turn", roots: []string{f.workspace}}, commands...)
	report := hub.begin(t, [3]string{f.thread, "turn", "writer"}, script)
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)})
	hub.await(t, [3]string{f.thread, "turn", "writer"}, func(r segments.Report) bool { return len(r.Segments) > 0 && r.Segments[0].Ended })
	writeTestFile(t, path, "after\n")
	broker := newLivePreview(t.Context())
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{f.workspace: {f.thread: true}}})
	sub := testReview(broker).Subscribe()
	tracking := proxy.Windows.find("writer").tracking
	capture := &execPreviewCapture{registry: proxy.Windows, ref: "writer", observation: *observed}
	frame := previewmodel.Preview{Workspace: f.workspace, Thread: f.thread, Turn: "turn"}
	capture.update(f.ctx, broker, frame, tracking, tracking.state())
	first := testReview(broker).TakePreviews(sub)
	first = slices.DeleteFunc(first, func(event sessionreview.Event) bool { return event.Preview == nil })
	if len(first) != 1 || first[0].Preview.SourceAnchor != (activitydetail.SourceAnchor{Call: "writer", Segment: 1}) {
		t.Fatalf("first writer boundary missing: %+v", first)
	}
	// Formatting can leave the same bytes and counts. Its observed boundary
	// must still replace the earlier anchor before the pending test.
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 1}, segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(0)})
	hub.await(t, [3]string{f.thread, "turn", "writer"}, func(r segments.Report) bool { return len(r.Segments) > 1 && r.Segments[1].Ended })
	want := activitydetail.SourceAnchor{Call: "writer", Segment: 2}
	for _, running := range []bool{false, true} {
		if running {
			report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 2})
			hub.await(t, [3]string{f.thread, "turn", "writer"}, func(r segments.Report) bool { return r.LastStarted() == 2 })
		}
		state := tracking.state()
		if state.anchor != want {
			t.Fatalf("test running=%v: source anchor=%+v, want %+v", running, state.anchor, want)
		}
		capture.update(f.ctx, broker, frame, tracking, state)
	}
	var live *previewmodel.Preview
	for _, event := range testReview(broker).TakePreviews(sub) {
		if event.Preview != nil && event.Preview.EditReceipt != "" {
			live = event.Preview
		}
	}
	if live == nil || live.SourceAnchor != want || !strings.Contains(live.EditReceipt, "+1 -1") {
		t.Fatalf("live receipt lost writer boundary: %+v", live)
	}
	if !proxy.Windows.find("writer").closed.IsZero() {
		t.Fatal("observed receipt completed the host")
	}
	report.send(segmenttransport.Message{Type: segmenttransport.End, Index: 2, Code: new(7)}, segmenttransport.Message{Type: segmenttransport.Done, Code: new(7)}, segmenttransport.Message{Type: segmenttransport.Closed})
	hub.await(t, [3]string{f.thread, "turn", "writer"}, func(r segments.Report) bool { return r.Complete() })
	h := mekugiHistory{ToolName: nativeExecCommandToolName, Root: f.workspace, ExecutingThread: f.thread, Script: script, ExecObservation: observed}
	err := finalizeExecObservations(proxy.Retained, proxy.Changes, proxy.Checkpoints, proxy.Recovery, proxy.PublishChanges, proxy.Windows, proxy.Audience, proxy.Notice, f.ctx, f.workspace, []execCompletion{{callID: "writer", history: h, result: binding.ExecutionResult{Terminal: true, ExitCode: new(7)}, host: &binding.ToolResult{CallID: "writer", Tool: "exec_command", Status: "completed", ExitCode: new(7)}}})
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := lookupObservation(f.ctx, proxy.Retained, f.workspace, legacycall.ExecCallID("writer", false))
	if err != nil || !found || record.sourceAnchor != want || record.ChangeID == "" || record.ExecOutcome.Exit == nil || *record.ExecOutcome.Exit != 7 {
		t.Fatalf("retained capture lost boundary or failure: found=%v err=%v record=%+v", found, err, record)
	}
	// A new projection, with no watcher or process, must publish the same anchor.
	projection := sessionreview.Projection{Retained: proxy.Retained, Changes: proxy.Changes}
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{f.workspace: {f.thread: true}}}
	if _, err := projection.Apply(f.ctx, sessionreview.Event{Kind: "scope", Scope: &scope}); err != nil {
		t.Fatal(err)
	}
	receipts := projection.Data.Receipts()
	if len(receipts) != 1 || receipts[0].SourceAnchor != want || !strings.Contains(receipts[0].Text, "+1 -1") {
		t.Fatalf("cold review lost source anchor: %+v", receipts)
	}
}
