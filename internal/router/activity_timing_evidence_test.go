package router

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
)

func TestActivitySingleCommandActualTime(t *testing.T) {
	t.Parallel()
	for _, script := range []string{"sleep .02", "time sleep .02", "bash -c 'sleep .02; printf ok; exit 7'", "printf ok | cat"} {
		t.Run(script, func(t *testing.T) {
			shell := newExecTrackShell(t)
			key := [3]string{"thread", "turn", "single"}
			timed := strings.HasPrefix(script, "time ")
			if timed {
				// Registration is outside the command clock, even when slow.
				timer := time.AfterFunc(100*time.Millisecond, func() { shell.hub.start(key, "bash -lc "+quoteShellWord(script)) })
				t.Cleanup(func() { timer.Stop() })
			} else {
				shell.hub.start(key, "bash -lc "+quoteShellWord(script))
			}
			got := runExecTrackShell(t, append(shell.env, "TIMEFORMAT=%R"), script)
			plain := runExecTrackShell(t, append(shell.env, "MEKUGI_EXEC_TRACK=1", "TIMEFORMAT=%R"), script)
			view := shell.awaitView(t, key)
			if !view.complete || view.output || len(view.segments) != 1 || got.code != plain.code || got.stdout != plain.stdout {
				t.Fatalf("host result changed: tracked=%+v plain=%+v view=%+v", got, plain, view)
			}
			elapsed := time.Duration(view.segments[0].timing.ElapsedNS)
			if timed {
				seconds, err := strconv.ParseFloat(strings.TrimSpace(got.stderr), 64)
				if err != nil || elapsed < 15*time.Millisecond || elapsed > time.Duration(seconds*float64(time.Second))+50*time.Millisecond {
					t.Fatalf("command=%s shell time=%q err=%v", elapsed, got.stderr, err)
				}
			} else if got.stderr != plain.stderr {
				t.Fatalf("stderr changed: %q vs %q", got.stderr, plain.stderr)
			}
			entry := activityPaneEntry{Kind: "tool", native: &liveActivityNativeItem{thread: key[0], turn: key[1], item: key[2], command: script, duration: time.Second, segments: view.segments}, outputTail: []string{got.stdout}}
			blocks := parseLiveActivity(entry)
			if elapsed <= 0 || len(blocks) != 1 || blocks[0].Duration != elapsed || blocks[0].NotificationTiming || blocks[0].ExitCode != got.code {
				t.Fatalf("command timing replaced by host time: %+v", blocks)
			}
			if !timed {
				return
			}
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			u := newAppServerSessionTestUI(t, t.TempDir())
			u.proxy = &mekugiProxy{replayStore: store}
			item := appServerItem{ID: key[2], Type: "commandExecution", Command: "bash -lc " + quoteShellWord(script), ExitCode: new(got.code), AggregatedOutput: new(got.stdout + got.stderr)}
			u.retainCommandSegments(entry, item, view)
			u.finishCommandSegments()
			entry.native.segments = nil
			u.restoreCommandSegments(&entry, item, u.session.cwd)
			blocks = parseLiveActivity(entry)
			if len(blocks) != 1 || blocks[0].Duration != elapsed || blocks[0].Running {
				t.Fatalf("single command timing lost on restore: %+v", blocks)
			}
		})
	}
}

func TestActivityTimingRealShellBoundariesAndRestart(t *testing.T) {
	t.Parallel()
	shell := newExecTrackShell(t)
	workspace, directory := t.TempDir(), t.TempDir()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, workspace)
	u.execTrack, u.proxy = shell.hub, &mekugiProxy{replayStore: store}
	script := "sleep .04; sleep .12; false && echo never"
	item := appServerItem{ID: "timed", Type: "commandExecution", Command: "/usr/bin/bash -lc " + quoteShellWord(script), Status: "inProgress"}
	key := [3]string{"main", "timed-turn", item.ID}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	result := runExecTrackShell(t, append(shell.env, "CODEX_THREAD_ID=main"), script)
	if result.code != 1 || result.stdout != "" || result.stderr != "" {
		t.Fatalf("host result: %+v", result)
	}
	observed := shell.awaitView(t, key)
	if !observed.complete || len(observed.segments) != 4 {
		t.Fatalf("segments: %+v", observed)
	}
	for i := range 3 {
		timing := observed.segments[i].timing
		if timing.Started.IsZero() || timing.Ended.IsZero() || timing.ElapsedNS <= 0 || timing.Ended.Before(timing.Started) {
			t.Fatalf("segment %d missing boundaries: %+v", i, timing)
		}
	}
	short, long := observed.segments[0].timing.ElapsedNS, observed.segments[1].timing.ElapsedNS
	if short < int64(20*time.Millisecond) || long < int64(80*time.Millisecond) || long == short {
		t.Fatalf("distinct actual durations: %s, %s", time.Duration(short), time.Duration(long))
	}
	if observed.segments[2].exit != 1 || !observed.segments[3].skipped || observed.segments[3].timing != (execsegment.Timing{}) {
		t.Fatalf("failed/skipped boundaries: %+v", observed.segments)
	}
	item.Status, item.ExitCode, item.AggregatedOutput = "failed", new(1), new("")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": key[0], "turnId": key[1], "item": item})
	awaitMain(t, u, "skipped")
	u.finishCommandSegments() // Normal shutdown drains asynchronous retained reports.
	store, err = openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	restored := newAppServerSessionTestUI(t, workspace)
	restored.proxy = &mekugiProxy{replayStore: store}
	restored.restoreHistory([]appServerHistoryTurn{{ID: key[1], Status: "completed", Items: []appServerItem{item}}})
	for _, entry := range restored.view.entries {
		if entry.CallID != item.ID || entry.native == nil {
			continue
		}
		segments := entry.native.segments
		if len(segments) != 4 {
			t.Fatalf("restored segments: %+v", segments)
		}
		for i := range 3 {
			if segments[i].timing != observed.segments[i].timing {
				t.Fatalf("segment %d timing changed on restart: %+v vs %+v", i, segments[i].timing, observed.segments[i].timing)
			}
		}
		if segments[3].timing != (execsegment.Timing{}) {
			t.Fatalf("skipped acquired timing: %+v", segments[3].timing)
		}
		blocks := commandSegmentBlocks(entry.activityPaneEntry)
		for _, block := range blocks {
			if block.Code == "sleep .04" && block.Duration != time.Duration(short) {
				t.Fatalf("short block duration: %+v", block)
			}
			if block.Code == "sleep .12" && block.Duration != time.Duration(long) {
				t.Fatalf("long block duration: %+v", block)
			}
		}
		return
	}
	t.Fatal("restored invocation absent")
}

func TestActivityTimingDoesNotBorrowHostTotalWithoutMessageEvidence(t *testing.T) {
	t.Parallel()
	entry := activityPaneEntry{Kind: "tool", Text: "Run `sleep .04`\n\nRun `sleep .12`", Observed: time.Now().Add(-5 * time.Second), native: &liveActivityNativeItem{command: "sleep .04; sleep .12", duration: 5 * time.Second, segments: []commandSegment{{source: "sleep .04", text: "Run `sleep .04`"}, {source: "sleep .12", text: "Run `sleep .12`"}}}}
	for _, block := range parseLiveActivity(entry) {
		if block.Duration != 0 || !block.Started.IsZero() || !block.Ended.IsZero() {
			t.Fatalf("borrowed host total: %+v", block)
		}
	}
}

func TestActivityTimingKeepsSeparateClassifiedReadCommands(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	first := execsegment.Timing{Started: start, Ended: start.Add(8 * time.Millisecond), ElapsedNS: int64(8 * time.Millisecond)}
	second := execsegment.Timing{Started: start.Add(10 * time.Millisecond), Ended: start.Add(31 * time.Millisecond), ElapsedNS: int64(21 * time.Millisecond)}
	entry := activityPaneEntry{Kind: "tool", native: &liveActivityNativeItem{command: "cat first.txt; cat second.txt", segments: []commandSegment{
		{source: "cat first.txt", text: "Read `first.txt`", timing: first},
		{source: "cat second.txt", text: "Read `second.txt`", timing: second},
	}}}
	blocks := commandSegmentBlocks(entry)
	if len(blocks) != 2 || blocks[0].Duration != 8*time.Millisecond || blocks[1].Duration != 21*time.Millisecond || blocks[0].Started != first.Started || blocks[1].Ended != second.Ended {
		t.Fatalf("classified reads merged or mistimed: %+v", blocks)
	}
}

func TestActivityTimingExitAndErrexitBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, script string
		ending       int
		code         int
	}{
		{"exit", "sleep .01; exit 7; echo never", 1, 7},
		{"errexit", "set -e; sleep .01; false; echo never", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shell := newExecTrackShell(t)
			key := [3]string{"thread", "turn", tc.name}
			shell.hub.start(key, "/usr/bin/bash -lc "+quoteShellWord(tc.script))
			result := runExecTrackShell(t, shell.env, tc.script)
			view := shell.awaitView(t, key)
			if result.code != tc.code || !view.complete || view.code != tc.code || len(view.segments) <= tc.ending+1 {
				t.Fatalf("host=%+v view=%+v", result, view)
			}
			exiting := view.segments[tc.ending]
			if exiting.exit != tc.code || exiting.timing.Started.IsZero() || exiting.timing.Ended.IsZero() || exiting.timing.ElapsedNS <= 0 {
				t.Fatalf("exiting command lacks timing: %+v", exiting)
			}
			skipped := view.segments[tc.ending+1]
			if !skipped.skipped || skipped.timing != (execsegment.Timing{}) {
				t.Fatalf("skipped command acquired timing: %+v", skipped)
			}
		})
	}
}

func TestActivityTimingLiveEvidenceDoesNotInheritHostTotal(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	track := &execTrack{segments: []execTrackSegment{{source: "sleep .01"}, {source: "sleep .02"}}}
	track.apply(execsegment.Message{Type: execsegment.Begin, Index: 0, Timing: execsegment.Timing{Started: start}})
	track.apply(execsegment.Message{Type: execsegment.End, Index: 0, Code: new(0), Timing: execsegment.Timing{Started: start, Ended: start.Add(12 * time.Millisecond), ElapsedNS: int64(12 * time.Millisecond)}})
	track.apply(execsegment.Message{Type: execsegment.Begin, Index: 1, Timing: execsegment.Timing{Started: start.Add(13 * time.Millisecond)}})
	hub := &execTrackHub{tracks: map[[3]string]*execTrack{{"thread", "turn", "live"}: track}}
	view, _ := hub.view([3]string{"thread", "turn", "live"}, false, func(source string) string { return "Run `" + source + "`" }, nil)
	entry := activityPaneEntry{Kind: "tool", Observed: start.Add(-5 * time.Second), native: &liveActivityNativeItem{command: "sleep .01; sleep .02", running: true, duration: 5 * time.Second, segments: view.segments}}
	blocks := parseLiveActivity(entry)
	if len(blocks) != 2 || blocks[0].Duration != 12*time.Millisecond || blocks[0].Started != start || blocks[1].Duration != 0 || blocks[1].Started != start.Add(13*time.Millisecond) || !blocks[1].Ended.IsZero() {
		t.Fatalf("live command evidence lost or host total borrowed: %+v", blocks)
	}
}
