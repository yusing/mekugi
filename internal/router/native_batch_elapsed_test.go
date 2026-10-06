package router

import (
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotNativeSingleCommandElapsed(t *testing.T) {
	start := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	entry := activityPaneEntry{Kind: "tool", native: &liveActivityNativeItem{command: "time skills-mgr get user-experience", duration: 100 * time.Millisecond,
		segments: []commandSegment{{source: "time skills-mgr get user-experience", text: "Run `time skills-mgr get user-experience`", timing: execsegment.Timing{Started: start, Ended: start.Add(20 * time.Millisecond), ElapsedNS: int64(20 * time.Millisecond)}}}}}
	var p activityui.Painter
	uisnapshot.Assert(t, "testdata/snapshots/native-single-command-elapsed.txt", strings.Join(p.Block(parseLiveActivity(entry)[0], 80), "\n")+"\n")
}

func TestUISnapshotNativeBatchElapsed(t *testing.T) {
	for _, running := range []bool{true, false} {
		for _, width := range []int{48, 100} {
			t.Run(fmt.Sprintf("running=%v/width=%d", running, width), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					const elapsed = 191129 * time.Millisecond
					start := time.Now().Add(-elapsed)
					entry := activityPaneEntry{Seq: 1, Kind: "tool", Text: "Run `make test TEST_PACKAGES=./internal/router`\n\nDiff `amber1` · mchanges --net", Observed: start,
						native: &liveActivityNativeItem{command: "make test TEST_PACKAGES=./internal/router; mchanges amber1 --net", running: running, commandStarted: start}}
					if !running {
						entry.native.duration, entry.native.commandEnded = elapsed, time.Now()
					}
					blocks := parseLiveActivity(entry)
					if len(blocks) != 3 || !blocks[2].BatchExit || blocks[2].Started != start {
						t.Fatalf("missing invocation total: %+v", blocks)
					}
					for _, b := range blocks[:2] {
						if b.Duration != 0 || !b.Started.IsZero() {
							t.Fatal("batch timing attributed to an individual operation")
						}
					}
					var p activityui.Painter
					var rows []string
					for _, b := range blocks {
						rows = append(rows, p.Block(b, width)...)
					}
					uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/native-batch-elapsed-%v-%d.txt", running, width), strings.Join(rows, "\n")+"\n")
				})
			})
		}
	}
}

func TestNativeBatchElapsedFailureAndResume(t *testing.T) {
	for _, mode := range []string{"live", "resume", "child"} {
		t.Run(mode, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			item := appServerItem{ID: "batch", Type: "commandExecution", Command: "sleep 1; false", Status: "failed", ExitCode: new(1), DurationMS: new(int64(191129)), AggregatedOutput: new("failed\n")}
			view := u.view
			if mode == "child" {
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
				u.restoreActivityThread(appServerThreadInfo{ID: "child", Turns: []appServerHistoryTurn{{ID: "turn", Status: "completed", Items: []appServerItem{item}}}})
				view = u.agents
			} else if mode == "resume" {
				u.restoreHistory([]appServerHistoryTurn{{ID: "turn", Status: "completed", Items: []appServerItem{item}}})
			} else {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			}
			finishPacing(view)
			found := false
			for _, entry := range view.entries {
				for _, block := range entry.blocks {
					if block.BatchExit {
						if found || block.Duration != 191129*time.Millisecond || block.ExitCode != 1 {
							t.Fatalf("incorrect batch timing or exit: %+v", block)
						}
						found = true
					} else if block.Duration != 0 {
						t.Fatal("host total borrowed by command")
					}
				}
			}
			if !found {
				t.Fatal("missing timed batch result")
			}
		})
	}
}

func TestNativeCapturedBatchKeepsSingleElapsedResult(t *testing.T) {
	for _, exit := range []int{0, 1} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			status := "completed"
			if exit != 0 {
				status = "failed"
			}
			item := appServerItem{ID: "batch", Type: "commandExecution", Command: "printf new > a.txt; sleep 1", Status: status, ExitCode: new(exit), DurationMS: new(int64(1000)), AggregatedOutput: new("retained batch output\n")}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			finishPacing(u.view)
			data := newLiveDiffData()
			data.order = []string{"receipt"}
			for range 2 {
				data.attempts["receipt"] = liveDiffAttempt{receipt: &capturedActivityEdit{thread: "main", calls: []string{"batch"}, text: "Edit `a.txt` +1 -1 · printf"}}
				u.view.applyCapturedEdits(data)
				count := 0
				for _, entry := range u.view.entries {
					for _, block := range entry.blocks {
						if block.BatchExit {
							count++
							if block.Duration != time.Second || block.ExitCode != exit || block.Output == nil || strings.Join(block.Output.View().Lines, "\n") != "retained batch output" {
								t.Fatalf("receipt lost batch timing, exit or output: %+v", block)
							}
						} else if block.Duration != 0 || block.ExitCode != 0 {
							t.Fatal("receipt attributed batch evidence to a command")
						}
					}
				}
				if count != 1 {
					t.Fatalf("got %d batch results, want 1", count)
				}
			}
		})
	}
}
