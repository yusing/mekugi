package capturer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAXFailureEvidenceAndExplicitExclusions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	for _, tc := range []struct{ thread, class string }{{"child", "invalid_arguments"}, {"other", "not_found"}, {"", "retained_file"}} {
		observation, err := StartAXReadWithContext(path, tc.thread, "hcat", AXReadContext{CallID: "call-shell", ShellID: "shell-1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := observation.FinishResult(false, tc.class, new(1)); err != nil {
			t.Fatal(err)
		}
	}
	reads, err := ReadAXReads(t.Context(), path, "child")
	if err != nil || reads.Failed != 1 || reads.OtherThreadStarted != 1 || reads.UnattributedStarted != 1 || reads.FailuresByClass["invalid_arguments"] != 1 {
		t.Fatalf("%+v: %v", reads, err)
	}
	failure := reads.Failures[0]
	if failure.CallID != "call-shell" || failure.ShellID != "shell-1" || failure.ID == "" || failure.ExitCode == nil || *failure.ExitCode != 1 {
		t.Fatalf("lost correlation: %+v", failure)
	}
	empty, err := ReadAXReads(t.Context(), path, "")
	if err != nil || empty.State != "unavailable" || empty.Started != 0 || empty.OtherThreadStarted != 2 || empty.UnattributedStarted != 1 {
		t.Fatalf("anonymous reads became attributed: %+v %v", empty, err)
	}
}

func TestAXRejectsUnsupportedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	event := axReadEvent{Schema: "mekugi.ax.read.v1", ID: "old", ThreadID: "thread", Tool: "hcat", Phase: "start", At: time.Now()}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(event); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reads, err := ReadAXReads(t.Context(), path, "thread")
	if err == nil || reads.State != "unavailable" {
		t.Fatalf("accepted unsupported schema: %+v %v", reads, err)
	}
}

func TestAXRejectsUnsafeDiagnosticAndMismatchedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	observation, err := StartAXReadWithContext(path, "thread", "hcat", AXReadContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := observation.FinishResult(false, "private /source/path and stderr", new(1)); err == nil {
		t.Fatal("accepted raw error as class")
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "private") {
		t.Fatal("raw diagnostic leaked")
	}
	reads, err := ReadAXReads(t.Context(), path, "thread")
	if err != nil || reads.Incomplete != 1 || reads.Completed != 0 {
		t.Fatalf("invalid diagnostic became completed: %+v %v", reads, err)
	}
	for _, mutate := range []func(*axReadEvent){
		func(e *axReadEvent) { e.CallID = "other" },
		func(e *axReadEvent) { e.FailureClass = "secret" },
		func(e *axReadEvent) { e.Succeeded = new(true) },
		func(e *axReadEvent) { e.ExitCode = new(256) },
		func(e *axReadEvent) { e.Schema = "mekugi.ax.read.v1" },
	} {
		start := axReadEvent{Schema: "mekugi.ax.read.v2", ID: "id", ThreadID: "thread", Tool: "hcat", Phase: "start", At: time.Now(), CallID: "call"}
		finish := start
		finish.Phase, finish.Succeeded, finish.DurationNS, finish.FailureClass = "finish", new(false), new(int64(1)), "reader_error"
		mutate(&finish)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		encoder := json.NewEncoder(file)
		_ = encoder.Encode(start)
		_ = encoder.Encode(finish)
		_ = file.Close()
		journal, err := ReadAXReadJournal(t.Context(), path)
		if err == nil || len(journal.Threads) != 0 {
			t.Fatalf("accepted inconsistent evidence: %+v %v", finish, err)
		}
	}
}

func TestAXFailureDetailsBoundDoesNotDropCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	for range 257 {
		observation, err := StartAXReadWithContext(path, "thread", "hcat", AXReadContext{})
		if err != nil {
			t.Fatal(err)
		}
		if err := observation.FinishResult(false, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	reads, err := ReadAXReads(context.Background(), path, "thread")
	if err != nil || reads.Failed != 257 || reads.FailuresByClass["unknown"] != 257 || len(reads.Failures) != 256 || reads.DroppedFailures != 1 {
		t.Fatalf("counts truncated: %+v %v", reads, err)
	}
}

func TestAXCommandIntervalsGapsAndMissingEvidence(t *testing.T) {
	var accumulator AXCommandAccumulator
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	observe := func(kind, id string, ms int64) {
		t.Helper()
		if err := accumulator.Observe(kind, id, "call-batch", at.Add(time.Duration(ms)*time.Millisecond), new(0)); err != nil {
			t.Fatal(err)
		}
	}
	observe("item_started", "program-1", 0)
	observe("item_completed", "program-1", 100)
	observe("item_started", "program-2", 350)
	observe("item_started", "overlap", 400)
	observe("item_completed", "program-2", 450)
	observe("item_completed", "overlap", 500)
	observe("item_completed", "no-start", 600)
	observe("item_started", "interrupted", 700)
	got := accumulator.Result()
	if got.Started != 4 || got.Completed != 4 || got.DurationMS != 300 || got.GapMS != 0 || got.UnpairedEvents != 2 {
		t.Fatalf("bad timing: %+v", got)
	}
	if got.Commands[1].GapBeforeMS != nil || got.Commands[2].GapBeforeMS != nil || got.Commands[3].DurationMS != nil || got.Commands[4].GapBeforeMS != nil {
		t.Fatalf("invented interval: %+v", got.Commands)
	}
	if got.Commands[0].LogicalCallID != "call-batch" {
		t.Fatal("lost logical call identity")
	}
	if empty := new(AXCommandAccumulator).Result(); empty.State != "unavailable" {
		t.Fatal("missing events became measured zero")
	}
}

func TestAXCommandGapsUseFinalEvidenceWithoutMutatingEarlierResults(t *testing.T) {
	var accumulator AXCommandAccumulator
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	observe := func(id string, start, end time.Duration) {
		t.Helper()
		if err := accumulator.ObserveCompleted(id, "", base.Add(start), base.Add(end), nil); err != nil {
			t.Fatal(err)
		}
	}
	observe("b", 20*time.Millisecond, 30*time.Millisecond)
	observe("c", 40*time.Millisecond, 50*time.Millisecond)
	earlier := accumulator.Result()
	observe("a", 0, 100*time.Millisecond)
	final := accumulator.Result()
	repeated := accumulator.Result()
	if earlier.GapMS != 10 || earlier.Commands[1].GapBeforeMS == nil || *earlier.Commands[1].GapBeforeMS != 10 ||
		final.GapMS != 0 || final.Commands[1].GapBeforeMS != nil ||
		repeated.GapMS != 0 || repeated.Commands[1].GapBeforeMS != nil {
		t.Fatalf("arrival order or result alias changed gaps: earlier=%+v final=%+v repeated=%+v", earlier, final, repeated)
	}
}
