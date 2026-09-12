package capturer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestAXReadJournalRestrictiveUmask(t *testing.T) {
	const marker = "MEKUGI_TEST_AX_UMASK_PATH"
	if path := os.Getenv(marker); path != "" {
		previous := syscall.Umask(0777)
		defer syscall.Umask(previous)
		if err := PrepareAXReadJournal(path); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("journal mode = %v, %v", info, err)
		}
		observation, err := StartAXRead(path, "thread", "hcat")
		if err != nil {
			t.Fatal(err)
		}
		if err := observation.Finish(true); err != nil {
			t.Fatal(err)
		}
		return
	}
	// Umask is process-wide, so exercise it outside the other package tests.
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAXReadJournalRestrictiveUmask$")
	command.Env = append(os.Environ(), marker+"="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restrictive umask: %v\n%s", err, output)
	}
	result, err := ReadAXReads(t.Context(), path, "thread")
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("journal = %+v, %v", result, err)
	}
}

func TestAXReadJournalExactSizeWithoutFinalNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	writer := bufio.NewWriter(file)
	const records = maxAXEvidenceBytes / 2048
	for i := range records {
		line := fmt.Sprintf(`{"schema":"mekugi.ax.read.v1","id":"%d","thread_id":"thread","tool":"hcat","phase":"start","at":"2026-09-11T00:00:00Z"}`, i)
		line += strings.Repeat(" ", 2047-len(line))
		if i == records-1 {
			line += " "
		} else {
			line += "\n"
		}
		if _, err := writer.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := ReadAXReads(t.Context(), path, "thread")
	if err != nil || result.Started != records || result.Incomplete != records {
		t.Fatalf("boundary journal = %+v, %v", result, err)
	}
	ctx := &axGrowingEvidenceContext{Context: t.Context(), grow: func() {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := file.WriteString("\n"); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := ReadAXReads(ctx, path, "thread"); err == nil || !strings.Contains(err.Error(), "exceeds 64 MiB") {
		t.Fatalf("growing journal accepted: %v", err)
	}
}

// Append after descriptor validation, at the first per-record cancellation check.
type axGrowingEvidenceContext struct {
	context.Context
	grow func()
}

func (ctx *axGrowingEvidenceContext) Err() error {
	if ctx.grow != nil {
		ctx.grow()
		ctx.grow = nil
	}
	return ctx.Context.Err()
}

func TestAXRuntimeReadJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	for _, test := range []struct {
		thread  string
		success bool
	}{{"thread", true}, {"thread", false}, {"other", true}} {
		observation, err := StartAXRead(path, test.thread, "hcat")
		if err != nil {
			t.Fatal(err)
		}
		if err := observation.Finish(test.success); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := StartAXRead(path, "thread", "inspect_file")
	if err != nil {
		t.Fatal(err)
	}
	pending.file.Close() // Model an interrupted worker that never publishes finish.
	result, err := ReadAXReads(t.Context(), path, "thread")
	if err != nil || result.Started != 3 || result.Completed != 2 || result.Succeeded != 1 ||
		result.Failed != 1 || result.Incomplete != 1 || result.ByTool["hcat"] != 2 || result.State != "observed" {
		t.Fatalf("read metrics = %+v, %v", result, err)
	}
	if got, err := ReadAXReads(t.Context(), "", "thread"); err != nil || got.State != "unavailable" {
		t.Fatalf("missing evidence = %+v, %v", got, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), path) || strings.Contains(string(data), "arguments") {
		t.Fatal("journal retained source details")
	}
}
func TestAXReadAcceptsBackwardWallClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	start := axReadEvent{Schema: "mekugi.ax.read.v1", ID: "id", ThreadID: "thread",
		Tool: "hcat", Phase: "start", At: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)}
	finish := start
	finish.Phase, finish.At = "finish", start.At.Add(-time.Second)
	finish.DurationNS, finish.Succeeded = new(int64(123)), new(true)
	first, err := json.Marshal(start)
	if err != nil {
		t.Fatal(err)
	}
	last, err := json.Marshal(finish)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append(first, '\n'), append(last, '\n')...), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadAXReads(t.Context(), path, "thread")
	if err != nil || result.Completed != 1 || result.Succeeded != 1 || result.DurationNS != 123 {
		t.Fatalf("read metrics = %+v, %v", result, err)
	}
}

func TestAXReadConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			observation, err := StartAXRead(path, "thread", "hgrep")
			if err != nil {
				t.Error(err)
				return
			}
			if err := observation.Finish(true); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	result, err := ReadAXReads(t.Context(), path, "thread")
	if err != nil || result.Started != 20 || result.Succeeded != 20 || result.Incomplete != 0 {
		t.Fatalf("concurrent metrics = %+v, %v", result, err)
	}
}

func TestAXReadRejectsUnsafeStorageAndMalformedEvents(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "target")
	if err := os.WriteFile(path, []byte("unchanged"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"relative", root, path, link} {
		if _, err := StartAXRead(invalid, "thread", "hcat"); err == nil {
			t.Fatalf("invalid journal %q accepted", invalid)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != "unchanged" {
		t.Fatal("unsafe target changed")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("unsafe target mode changed: %v, %v", info, err)
	}
	for _, data := range []string{"{}\n", "not-json\n", strings.Repeat("x", 5000)} {
		if err := os.WriteFile(filepath.Join(root, "bad"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadAXReads(t.Context(), filepath.Join(root, "bad"), "thread"); err == nil {
			t.Fatal("invalid event accepted")
		}
	}
}

func TestAXReadRejectsDuplicateAndUnpairedEvents(t *testing.T) {
	for _, phase := range []string{"start", "finish"} {
		path := filepath.Join(t.TempDir(), "reads.jsonl")
		event := axReadEvent{Schema: "mekugi.ax.read.v1", ID: "id", ThreadID: "thread",
			Tool: "hcat", Phase: phase, At: time.Now()}
		data, _ := json.Marshal(event)
		data = append(data, '\n')
		if phase == "start" {
			data = append(data, data...)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadAXReads(t.Context(), path, "thread"); err == nil {
			t.Fatalf("bad %s events accepted", phase)
		}
	}
}

func TestAXEditMeasurementsUseOriginalPayloadBytes(t *testing.T) {
	var edits AXEditAccumulator
	first := "in f\nold\nold\n"
	second := first + "old\n"
	edits.Observe(first, false, true, false)
	edits.Observe(second, true, true, false)
	edits.Observe(`type "old" "new"`, true, false, true)
	if edits.Metrics.Calls != 3 || edits.Metrics.RecoveryRetries != 2 ||
		edits.Metrics.ReEmittedLineBytes != uint64(len(first)) ||
		edits.Metrics.EmittedBytes != uint64(len(first)+len(second)+len(`type "old" "new"`)) ||
		edits.Metrics.Rejected != 2 || edits.Metrics.Unconfirmed != 1 {
		t.Fatalf("edit metrics = %+v", edits.Metrics)
	}
}

func TestAXCompletionUsesPairedObservedEvents(t *testing.T) {
	var completion AXCompletionAccumulator
	if completion.Result().State != "unavailable" {
		t.Fatal("missing completion claimed observed")
	}
	start := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	completion.Observe("task_started", "turn", start)
	completion.Observe("task_complete", "turn", start.Add(1500*time.Millisecond))
	completion.Observe("task_started", "turn", start)
	completion.Observe("task_complete", "turn", start.Add(time.Second))
	completion.Observe("turn_started", "incomplete", start)
	result := completion.Result()
	if result.CompletedTurns != 1 || result.DurationMS != 1500 || result.UnpairedEvents != 3 {
		t.Fatalf("completion = %+v", result)
	}
}

func TestAXCompletionRequiresMatchingEventKinds(t *testing.T) {
	for _, test := range []struct{ start, wrong, finish string }{
		{"task_started", "turn_complete", "task_complete"},
		{"turn_started", "task_complete", "turn_complete"},
		{"task_started", "unknown", "task_complete"},
	} {
		t.Run(test.start+"/"+test.wrong, func(t *testing.T) {
			var completion AXCompletionAccumulator
			start := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
			completion.Observe(test.start, "turn", start)
			completion.Observe(test.wrong, "turn", start.Add(time.Second))
			if result := completion.Result(); result.CompletedTurns != 0 || result.DurationMS != 0 || result.UnpairedEvents != 2 {
				t.Fatalf("cross-variant completion = %+v", result)
			}
			completion.Observe(test.finish, "turn", start.Add(2*time.Second))
			if result := completion.Result(); result.CompletedTurns != 1 || result.DurationMS != 2000 || result.UnpairedEvents != 1 {
				t.Fatalf("matching completion after unpaired event = %+v", result)
			}
		})
	}
}

func TestAXCompletionDurationSaturates(t *testing.T) {
	// Seed a large accumulated duration without retaining a million turn IDs.
	completion := AXCompletionAccumulator{Metrics: AXCompletionMetrics{DurationMS: math.MaxInt64 - 10}}
	start := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	for i, test := range []struct{ elapsed, want int64 }{
		{5, math.MaxInt64 - 5},
		{5, math.MaxInt64},
		{1, math.MaxInt64},
		{1000, math.MaxInt64},
	} {
		id := fmt.Sprint(i)
		completion.Observe("task_started", id, start)
		completion.Observe("task_complete", id, start.Add(time.Duration(test.elapsed)*time.Millisecond))
		if result := completion.Result(); result.DurationMS != test.want || result.CompletedTurns != uint64(i+1) || result.UnpairedEvents != 0 {
			t.Fatalf("pair %d: completion = %+v, want duration %d", i, result, test.want)
		}
	}
}

func TestAXDefectsRequireKnownCallsAndRealEvidence(t *testing.T) {
	root := t.TempDir()
	evidence := filepath.Join(root, "test-output.txt")
	if err := os.WriteFile(evidence, []byte("TestExample failed: expected 2, got 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "assessments.json")
	for _, test := range []struct {
		data  string
		valid bool
	}{
		{`[{"call_id":"edit","verdict":"defect","evidence":"test-output.txt"}]`, true},
		{`[{"call_id":"edit","verdict":"no_defect","evidence":"test-output.txt"}]`, true},
		{`[{"call_id":"unknown","verdict":"defect","evidence":"test-output.txt"}]`, false},
		{`[{"call_id":"edit","verdict":"maybe","evidence":"test-output.txt"}]`, false},
		{`[{"call_id":"edit","verdict":"defect","evidence":"missing"}]`, false},
		{`null`, false},
		{`[] {}`, false},
	} {
		if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := ReadAXDefects(path, map[string]bool{"edit": true, "unassessed": true})
		if !test.valid {
			if err == nil {
				t.Fatalf("invalid assessment accepted: %s", test.data)
			}
			continue
		}
		if err != nil || result.AssessedCalls != 1 || result.Unassessed != 1 ||
			len(result.Assessments[0].SHA256) != 64 || result.Assessments[0].Evidence != evidence {
			t.Fatalf("assessment = %+v, %v", result, err)
		}
	}
}

func TestAXValidatesJournalWithoutThreadMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	if _, err := ReadAXReads(t.Context(), path, ""); err == nil {
		t.Fatal("missing supplied journal accepted")
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAXReads(t.Context(), path, ""); err == nil {
		t.Fatal("malformed supplied journal accepted")
	}
}

func TestAXConcurrentNearCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxAXEvidenceBytes - 350); err != nil {
		t.Fatal(err)
	}
	file.Close()
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			observation, err := StartAXRead(path, "thread", "hcat")
			if err == nil {
				_ = observation.Finish(true)
			}
		})
	}
	workers.Wait()
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxAXEvidenceBytes {
		t.Fatalf("concurrent writers exceeded limit: %v, %v", info, err)
	}
}

func TestAXReadJournalRejectsCorruptUTF8IDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	start := `{"schema":"mekugi.ax.read.v1","id":"` + "\xff" + `","thread_id":"thread","tool":"hcat","phase":"start","at":"2026-09-11T00:00:00Z"}`
	finish := `{"schema":"mekugi.ax.read.v1","id":"` + "\xfe" + `","thread_id":"thread","tool":"hcat","phase":"finish","at":"2026-09-11T00:00:01Z","duration_ns":100,"succeeded":true}`
	if err := os.WriteFile(path, []byte(start+"\n"+finish+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	journal, err := ReadAXReadJournal(t.Context(), path)
	if err == nil || !strings.Contains(err.Error(), "UTF-8") || len(journal.Threads) != 0 {
		t.Fatalf("corrupt IDs were admitted: %+v, %v", journal, err)
	}
	reads, err := ReadAXReads(t.Context(), path, "thread")
	if err == nil || reads.Succeeded != 0 || reads.State != "unavailable" {
		t.Fatalf("corrupt evidence became successful reads: %+v, %v", reads, err)
	}
}
