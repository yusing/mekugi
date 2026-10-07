package activity

import (
	"slices"
	"strings"
	"testing"
)

func TestOutputRedrawAcrossChunks(t *testing.T) {
	var o Output
	for _, chunk := range []string{"first\r", "\n10%\r", "90%\r100%\n", "last\x1b[2K  \n\n"} {
		o.Write(chunk)
	}
	if got := o.View().Lines; !slices.Equal(got, []string{"first", "100%", "last"}) {
		t.Fatalf("lines = %q", got)
	}
}

func TestOutputSnapshotsReplaceWithoutInventingDeltas(t *testing.T) {
	var retention Retention
	o := retention.New()
	o.Snapshot("first\n", false)
	o.Snapshot("first\nsecond\n", false)
	if !slices.Equal(o.View().Lines, []string{"first", "second"}) || o.View().Done {
		t.Fatal("live snapshot appended duplicated output")
	}
	o.Snapshot("tail\n", true)
	view := o.View()
	if !view.PrefixOmitted || view.Dropped != 0 || !slices.Equal(view.Lines, []string{"tail"}) {
		t.Fatal("tail snapshot invented a line count or kept old bytes")
	}
	if retention.bytes != o.bytes+len(o.line) {
		t.Fatal("snapshot replacement leaked retention accounting")
	}
	o.Finish(new("full final output"), nil)
	if !o.View().Done || o.View().PrefixOmitted {
		t.Fatal("full aggregate inherited the tail limitation")
	}
	version := o.Version()
	o.Snapshot("late", true)
	if o.Version() != version || !slices.Equal(o.View().Lines, []string{"full final output"}) {
		t.Fatal("late snapshot altered completed output")
	}
}

func TestOutputBoundsAndDroppedLines(t *testing.T) {
	var o Output
	line := strings.Repeat("x", OutputLineBytes)
	count := OutputBytes/OutputLineBytes + 3
	o.Write(strings.Repeat(line+"\n", count))
	view := o.View()
	if o.bytes > OutputBytes || view.Dropped == 0 || len(view.Lines)+view.Dropped != count {
		t.Fatalf("bytes=%d dropped=%d lines=%d", o.bytes, view.Dropped, len(view.Lines))
	}
	var long Output
	long.Write(strings.Repeat("x", OutputLineBytes+100) + "\n")
	if got := long.View().Lines; len(got) != 1 || got[0] != line+"…" {
		t.Fatal("long line did not retain a bounded head and elision")
	}
}

func TestOutputFinishAggregateReplacement(t *testing.T) {
	var r Retention
	o := r.New()
	o.Write("streamed\npartial")
	aggregate := "authoritative\nresult"
	o.Finish(&aggregate, new(7))
	view := o.View()
	if !slices.Equal(view.Lines, []string{"authoritative", "result"}) || !view.Done || !view.Exited || view.Exit != 7 || view.Dropped != 0 {
		t.Fatalf("view = %+v", view)
	}
	if r.bytes <= len("authoritativeresult") || r.bytes > OutputBytes {
		t.Fatalf("retention charge = %d", r.bytes)
	}
	version := o.Version()
	o.Write("ignored")
	o.Finish(new("also ignored"), new(0))
	if o.Version() != version || !slices.Equal(o.View().Lines, view.Lines) {
		t.Fatal("settled output changed")
	}
	o.Reconcile("late native aggregate\n界cedar")
	view = o.View()
	if !view.Done || !view.Exited || view.Exit != 7 || !slices.Equal(view.Lines, []string{"late native aggregate", "界cedar"}) || r.bytes != o.bytes+len(o.line) {
		t.Fatalf("terminal reconciliation lost output, exit or retention: %+v", view)
	}
	o.Release()
	o.Reconcile("cannot revive released evidence")
	if !o.View().Released || len(o.View().Lines) != 0 {
		t.Fatal("reconciliation revived released output")
	}
	empty := r.New()
	empty.Write("discard")
	empty.Finish(new(""), nil)
	if got := empty.View(); len(got.Lines) != 0 || got.Exited || !got.Done {
		t.Fatalf("empty aggregate = %+v", got)
	}
}

func TestOutputReportsLineTruncation(t *testing.T) {
	for _, ending := range []string{"", "\n"} {
		var o Output
		o.Write(strings.Repeat("x", OutputLineBytes+1) + ending)
		if !o.View().Truncated || o.View().Dropped != 0 {
			t.Fatal("line truncation was not reported independently of dropped rows")
		}
		o.Finish(new("complete aggregate"), new(0))
		if o.View().Truncated {
			t.Fatal("replacement aggregate inherited discarded truncation")
		}
	}
}

func TestOutputRetentionReleasesSettledNotLive(t *testing.T) {
	var r Retention
	payload := strings.Repeat(strings.Repeat("x", OutputLineBytes)+"\n", OutputBytes/OutputLineBytes)
	live := r.New()
	live.Write(payload)
	oldest := r.New()
	oldest.Write(payload)
	oldest.Finish(nil, nil)
	for range OutputRetainedBytes/OutputBytes - 1 {
		o := r.New()
		o.Write(payload)
		o.Finish(nil, nil)
	}
	if live.View().Released || len(live.View().Lines) == 0 {
		t.Fatal("running output released")
	}
	if !oldest.View().Released || len(oldest.View().Lines) != 0 {
		t.Fatal("oldest settled output was not released")
	}
	if r.bytes > OutputRetainedBytes {
		t.Fatalf("retained %d bytes", r.bytes)
	}
}

func TestOutputBoundsBlankLines(t *testing.T) {
	var o Output
	o.Write(strings.Repeat("\n", OutputBytes))
	view := o.View()
	if o.bytes > OutputBytes || view.Dropped == 0 || len(o.lines)+view.Dropped != OutputBytes {
		t.Fatalf("blank lines: bytes=%d dropped=%d kept=%d", o.bytes, view.Dropped, len(view.Lines))
	}
}

func TestOutputFinishKeepsByteBound(t *testing.T) {
	var o Output
	line := strings.Repeat("x", OutputLineBytes)
	o.Write(strings.Repeat(line+"\n", OutputBytes/OutputLineBytes) + line)
	o.Finish(nil, nil)
	if o.bytes > OutputBytes {
		t.Fatalf("Finish retained %d bytes, limit %d", o.bytes, OutputBytes)
	}
}
