package mekugi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPreWriteObserverFormattedReviewAndIsolation(t *testing.T) {
	base := t.TempDir()
	original := "package sample\n\nvar Value = 1\n"
	writeTestFile(t, base, "sample.go", original, 0o644)
	var observed []ReviewFile
	calls := 0
	ctx := WithPreWriteObserver(t.Context(), func(files []ReviewFile) {
		calls++
		if got := readTestFile(t, base, "sample.go"); got != original {
			t.Fatalf("file changed before observation: %q", got)
		}
		observed = slices.Clone(files)
		if len(files) != 1 {
			t.Fatalf("review files: %+v", files)
		}
		files[0] = ReviewFile{Diff: "corrupted"}
	})
	result, err := ApplyForHostAt(ctx, base, []FileEdit{{Path: "sample.go", Script: `type "var Value = 1" "var   Value=2"`}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !slices.Equal(observed, result.ReviewFiles) {
		t.Fatalf("calls=%d observed=%+v result=%+v", calls, observed, result.ReviewFiles)
	}
	if !strings.Contains(observed[0].Diff, "+var Value = 2\n") {
		t.Fatalf("review is not formatted: %s", observed[0].Diff)
	}
	if got := readTestFile(t, base, "sample.go"); got != "package sample\n\nvar Value = 2\n" {
		t.Fatalf("final content: %q", got)
	}
}

func TestPreWriteObserverRejectedBatch(t *testing.T) {
	base := t.TempDir()
	writeTestFile(t, base, "first.txt", "first\n", 0o644)
	writeTestFile(t, base, "second.go", "package sample\n", 0o644)
	calls := 0
	ctx := WithPreWriteObserver(t.Context(), func([]ReviewFile) { calls++ })
	_, err := ApplyForHostAt(ctx, base, []FileEdit{
		{Path: "first.txt", Script: `type "first" "changed"`},
		{Path: "second.go", Script: `append "func broken("`},
	}, "")
	if err == nil || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if got := readTestFile(t, base, "first.txt"); got != "first\n" {
		t.Fatalf("partial write: %q", got)
	}
	if got := readTestFile(t, base, "second.go"); got != "package sample\n" {
		t.Fatalf("invalid file written: %q", got)
	}
}

func TestPreWriteObserverNoOp(t *testing.T) {
	base := t.TempDir()
	writeTestFile(t, base, "same.txt", "same\n", 0o644)
	calls := 0
	ctx := WithPreWriteObserver(t.Context(), func(files []ReviewFile) {
		calls++
		if len(files) != 0 {
			t.Fatalf("no-op review: %+v", files)
		}
	})
	result, err := ApplyForHostAt(ctx, base, []FileEdit{{Path: "same.txt", Script: `type "same" "same"`}}, "")
	if err != nil || calls != 1 || !result.Change.AlreadySatisfied {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
}

func TestPreWriteObserverCancellationPreventsCommit(t *testing.T) {
	base := t.TempDir()
	writeTestFile(t, base, "file.txt", "before", 0o644)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	ctx = WithPreWriteObserver(ctx, func([]ReviewFile) {
		calls++
		cancel()
	})
	result, err := ApplyForHostAt(ctx, base, []FileEdit{{Path: "file.txt", Script: `type "before" "after"`}}, "")
	if !errors.Is(err, context.Canceled) || calls != 1 || result.Change.Applied || len(result.ReviewFiles) != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	if got := readTestFile(t, base, "file.txt"); got != "before" {
		t.Fatalf("canceled write: %q", got)
	}
}

func TestPreWriteObserverNotCalledByReadOnlyEvaluation(t *testing.T) {
	base := t.TempDir()
	writeTestFile(t, base, "file.txt", "before", 0o644)
	calls := 0
	ctx := WithPreWriteObserver(t.Context(), func([]ReviewFile) { calls++ })
	edits := []FileEdit{{Path: "file.txt", Script: `type "before" "after"`}}
	if _, err := TranslateForHostAt(ctx, base, edits, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewForHostAt(ctx, base, edits); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || readTestFile(t, base, "file.txt") != "before" {
		t.Fatalf("read-only evaluation invoked observer %d times or wrote file", calls)
	}
}
