package router

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/commandsegments/segments"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func BenchmarkActivityTimingParse(b *testing.B) {
	for _, entry := range []sessionpresentation.ActivityEntry{
		{Kind: "text", Text: "Completed the focused checks", Native: &sessionpresentation.NativeItem{}},
		{Kind: "tool", Text: "Run `go test ./...`", Native: &sessionpresentation.NativeItem{Command: "go test ./...", Running: true}},
		{Kind: "tool", Text: "Run `echo one`\n\nRun `echo two`", Native: &sessionpresentation.NativeItem{Command: "echo one; echo two", Running: true}},
	} {
		b.Run(entry.Kind+"/"+entry.Native.Command, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if len(parseLiveActivity(entry)) == 0 {
					b.Fatal("activity disappeared")
				}
			}
		})
	}
}

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
			elapsed := time.Duration(view.segments[0].Timing.ElapsedNS)
			if timed {
				seconds, err := strconv.ParseFloat(strings.TrimSpace(got.stderr), 64)
				if err != nil || elapsed < 15*time.Millisecond || elapsed > time.Duration(seconds*float64(time.Second))+50*time.Millisecond {
					t.Fatalf("command=%s shell time=%q err=%v", elapsed, got.stderr, err)
				}
			} else if got.stderr != plain.stderr {
				t.Fatalf("stderr changed: %q vs %q", got.stderr, plain.stderr)
			}
			entry := sessionpresentation.ActivityEntry{Kind: "tool", Native: &sessionpresentation.NativeItem{Thread: key[0], Turn: key[1], Item: key[2], Command: script, Duration: time.Second, Segments: view.segments}, OutputTail: []string{got.stdout}}
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
			setTestAppServerIntegration(u, testProxyWithStore(store))
			item := binding.ThreadItem{ID: key[2], Type: "commandExecution", Command: "bash -lc " + quoteShellWord(script), ExitCode: new(got.code), AggregatedOutput: new(got.stdout + got.stderr)}
			u.retainCommandSegments(entry, item.Observation(), view)
			u.finishCommandSegments()
			entry.Native.Segments = nil
			u.restoreCommandSegments(&entry, item, u.cwd)
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
	u.execTrack = shell.hub.Observer
	setTestUIStore(u, store)
	shell.hub.ui = u
	script := "sleep .04; sleep .12; false && echo never"
	item := binding.ThreadItem{ID: "timed", Type: "commandExecution", Command: "/usr/bin/bash -lc " + quoteShellWord(script), Status: "inProgress"}
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
		timing := observed.segments[i].Timing
		if timing.Started.IsZero() || timing.Ended.IsZero() || timing.ElapsedNS <= 0 || timing.Ended.Before(timing.Started) {
			t.Fatalf("segment %d missing boundaries: %+v", i, timing)
		}
	}
	short, long := observed.segments[0].Timing.ElapsedNS, observed.segments[1].Timing.ElapsedNS
	if short < int64(20*time.Millisecond) || long < int64(80*time.Millisecond) || long == short {
		t.Fatalf("distinct actual durations: %s, %s", time.Duration(short), time.Duration(long))
	}
	if observed.segments[2].Exit != 1 || !observed.segments[3].Skipped || observed.segments[3].Timing != (segmenttransport.Timing{}) {
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
	setTestAppServerIntegration(restored, testProxyWithStore(store))
	restored.restoreHistory([]binding.HistoryTurn{{ID: key[1], Status: "completed", Items: []binding.ThreadItem{item}}})
	for _, entry := range restored.source.Entries {
		if entry.Source.CallID != item.ID || entry.Source.Native == nil {
			continue
		}
		segments := entry.Source.Native.Segments
		if len(segments) != 4 {
			t.Fatalf("restored segments: %+v", segments)
		}
		for i := range 3 {
			if segments[i].Timing != observed.segments[i].Timing {
				t.Fatalf("segment %d timing changed on restart: %+v vs %+v", i, segments[i].Timing, observed.segments[i].Timing)
			}
		}
		if segments[3].Timing != (segmenttransport.Timing{}) {
			t.Fatalf("skipped acquired timing: %+v", segments[3].Timing)
		}
		blocks := commandSegmentBlocks(sessionpresentation.CommandPresentation(entry.Source))
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
	entry := sessionpresentation.ActivityEntry{Kind: "tool", Text: "Run `sleep .04`\n\nRun `sleep .12`", Observed: time.Now().Add(-5 * time.Second), Native: &sessionpresentation.NativeItem{Command: "sleep .04; sleep .12", Duration: 5 * time.Second, Segments: []sessionpresentation.ActivitySegment{{Source: "sleep .04", Text: "Run `sleep .04`"}, {Source: "sleep .12", Text: "Run `sleep .12`"}}}}
	for _, block := range parseLiveActivity(entry) {
		if block.Duration != 0 || !block.Started.IsZero() || !block.Ended.IsZero() {
			t.Fatalf("borrowed host total: %+v", block)
		}
	}
}

func TestActivityTimingKeepsSeparateClassifiedReadCommands(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	first := segmenttransport.Timing{Started: start, Ended: start.Add(8 * time.Millisecond), ElapsedNS: int64(8 * time.Millisecond)}
	second := segmenttransport.Timing{Started: start.Add(10 * time.Millisecond), Ended: start.Add(31 * time.Millisecond), ElapsedNS: int64(21 * time.Millisecond)}
	entry := sessionpresentation.ActivityEntry{Kind: "tool", Native: &sessionpresentation.NativeItem{Command: "cat first.txt; cat second.txt", Segments: []sessionpresentation.ActivitySegment{
		{Source: "cat first.txt", Text: "Read `first.txt`", Timing: first},
		{Source: "cat second.txt", Text: "Read `second.txt`", Timing: second},
	}}}
	blocks := commandSegmentBlocks(sessionpresentation.CommandPresentation(entry))
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
		{"errexit", "set -e; sleep .01; false; echo never", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shell := newExecTrackShell(t)
			key := [3]string{"thread", "turn", tc.name}
			shell.hub.start(key, "/usr/bin/bash -lc "+quoteShellWord(tc.script))
			result := runExecTrackShell(t, shell.env, tc.script)
			view := shell.awaitView(t, key)
			if result.code != tc.code || !view.complete || view.code != tc.code || len(view.segments) <= tc.ending {
				t.Fatalf("host=%+v view=%+v", result, view)
			}
			exiting := view.segments[tc.ending]
			if exiting.Exit != tc.code || exiting.Timing.Started.IsZero() || exiting.Timing.Ended.IsZero() || exiting.Timing.ElapsedNS <= 0 {
				t.Fatalf("exiting command lacks timing: %+v", exiting)
			}
			if tc.name == "errexit" {
				if len(view.segments) != 1 || exiting.Source != tc.script || result.stdout != "" {
					t.Fatalf("setup event split or ran after errexit: host=%+v view=%+v", result, view)
				}
				return
			}
			skipped := view.segments[tc.ending+1]
			if !skipped.Skipped || skipped.Timing != (segmenttransport.Timing{}) {
				t.Fatalf("skipped command acquired timing: %+v", skipped)
			}
		})
	}
}

func TestActivityTimingLiveEvidenceDoesNotInheritHostTotal(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	key := [3]string{"thread", "turn", "live"}
	hub := newExecTrackFixture(t, t.Context(), segments.Instrumentation{})
	report := hub.begin(t, key, "sleep .01; sleep .02")
	report.send(
		segmenttransport.Message{Type: segmenttransport.Begin, Index: 0, Timing: segmenttransport.Timing{Started: start}},
		segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0), Timing: segmenttransport.Timing{Started: start, Ended: start.Add(12 * time.Millisecond), ElapsedNS: int64(12 * time.Millisecond)}},
		segmenttransport.Message{Type: segmenttransport.Begin, Index: 1, Timing: segmenttransport.Timing{Started: start.Add(13 * time.Millisecond)}},
	)
	view := hub.await(t, key, func(view execTrackView) bool { return len(view.segments) == 2 })
	entry := sessionpresentation.ActivityEntry{Kind: "tool", Observed: start.Add(-5 * time.Second), Native: &sessionpresentation.NativeItem{Command: "sleep .01; sleep .02", Running: true, Duration: 5 * time.Second, Segments: view.segments}}
	blocks := parseLiveActivity(entry)
	if len(blocks) != 2 || blocks[0].Duration != 12*time.Millisecond || blocks[0].Started != start || blocks[1].Duration != 0 || blocks[1].Started != start.Add(13*time.Millisecond) || !blocks[1].Ended.IsZero() {
		t.Fatalf("live command evidence lost or host total borrowed: %+v", blocks)
	}
}
