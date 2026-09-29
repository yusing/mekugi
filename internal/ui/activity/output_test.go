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
	empty := r.New()
	empty.Write("discard")
	empty.Finish(new(""), nil)
	if got := empty.View(); len(got.Lines) != 0 || got.Exited || !got.Done {
		t.Fatalf("empty aggregate = %+v", got)
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
