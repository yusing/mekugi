package presentation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/evidence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestBoundedReceiptDoesNotInventOmittedRowCounts(t *testing.T) {
	file := diffmodel.RenderReviewFile("", "/w/big.txt", "", strings.Repeat("01234567890123456789012345678901234567890123456789\n", 1000))
	full := NewReceipt("/w", ReceiptEvidence{Files: []diffmodel.ReviewFile{file}})
	if !strings.Contains(full.Text, "… 921 more diff lines") {
		t.Fatal("fully available receipt lost exact omission count")
	}
	service, err := history.NewService(evidence.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := service.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked=%v", chunked), func(t *testing.T) {
			review := evidence.Inline(file)
			if chunked {
				budget := 0
				review = evidence.RetainReview(t.Context(), &evidence.Source{Store: store}, file, &budget)
				if review.Text == nil {
					t.Fatal("fixture did not retain chunked text")
				}
			}
			for _, limit := range []int{MaxReceiptDiffBytes, 16, 0, len(file.Diff) - 1, len(file.Diff)} {
				files, counts, truncated, err := evidence.ReceiptFiles([]evidence.Review{review}, limit)
				if err != nil {
					t.Fatal(err)
				}
				if len(files[0].Diff) > limit {
					t.Fatalf("detached bytes=%d limit=%d", len(files[0].Diff), limit)
				}
				receipt := NewReceipt("/w", ReceiptEvidence{Files: files, Counts: counts, Truncated: truncated})
				if !strings.Contains(receipt.Text, "+1000 -0") {
					t.Fatal("bounded receipt lost exact added/removed totals")
				}
				if limit < len(file.Diff) {
					if !truncated[0] || !strings.Contains(receipt.Text, "… more diff content") {
						t.Fatalf("limit=%d lost unknown omission count", limit)
					}
				} else if truncated[0] || receipt.Text != full.Text {
					t.Fatal("complete detached receipt changed fully available behavior")
				}
			}
		})
	}
}

func TestReceiptIntervalsPreserveExcerptTruncation(t *testing.T) {
	small := liveDiffNetCapture(1, "/root", "", "/w/small", "", "small\n")
	large := liveDiffNetCapture(2, "/root", "", "/w/large", "", strings.Repeat("long added source row\n", 1000))
	first := snapshotAttempt("interval", "thread", "writer", small)
	first.Receipt = NewReceipt("/w", ReceiptEvidence{Files: []diffmodel.ReviewFile{small.Review}})
	files, counts, truncated, err := evidence.ReceiptFiles([]evidence.Review{evidence.Inline(large.Review)}, MaxReceiptDiffBytes)
	if err != nil {
		t.Fatal(err)
	}
	second := snapshotAttempt("terminal", "thread", "writer", large)
	second.Receipt = NewReceipt("/w", ReceiptEvidence{Files: files, Counts: counts, Truncated: truncated})
	for _, attempt := range []*Attempt{&first, &second} {
		attempt.Receipt.Thread = "thread"
		attempt.Receipt.Calls = []string{"writer-command"}
	}
	snapshot := snapshotFixture(t, first, second)
	got := snapshot.Receipts()[0].Text
	if len(snapshot.Receipts()) != 1 || strings.Count(got, "… more diff content") != 1 || !strings.Contains(got, "+1000 -0") || !strings.Contains(got, "+1 -0") {
		t.Fatal("merged receipt lost bounded excerpt metadata or exact totals")
	}
}

// Source: apps/mekugi/internal/router/exec_managed_test.go:66:98@6f68e18a.
func TestManagedExecReceiptGroupsFiles(t *testing.T) {
	workspace := t.TempDir()
	files := make([]diffmodel.ReviewFile, 5)
	for i := range files {
		path := fmt.Sprintf("generated-%02d.txt", i+1)
		files[i] = diffmodel.RenderReviewFile("", path, "", "content\n")
		files[i].Origin = "generator"
	}
	got := ReceiptText(workspace, ReceiptEvidence{Files: files})
	want := ""
	if got != want {
		t.Fatalf("grouped receipt = %q, want %q", got, want)
	}
}

func TestExecReceiptOmitsSharedOriginNamedByLabels(t *testing.T) {
	workspace := t.TempDir()
	file := diffmodel.RenderReviewFile("a.go", "a.go", "package a\n\nfunc A() {}\n", "package a\n")
	file.OriginNote = "goimports" + changes.SharedOriginSuffix
	for _, test := range []struct {
		labels []string
		note   bool
	}{
		{labels: []string{"python3", "goimports"}},
		{labels: []string{"python3"}, note: true},
		{note: true},
	} {
		got := ReceiptText(workspace, ReceiptEvidence{Files: []diffmodel.ReviewFile{file}, Labels: test.labels})
		if strings.Contains(got, "(goimports also ran)") != test.note {
			t.Errorf("labels %q receipt = %q, want note %v", test.labels, got, test.note)
		}
	}
}

func TestGitDiagnosticReceiptCounts(t *testing.T) {
	files := []diffmodel.ReviewFile{diffmodel.RenderReviewFile("a", "a", "before\n", "after\n"), diffmodel.RenderReviewFile("", "b", "", "new\n")}
	for i := range files {
		files[i].Origin = "git reset"
	}
	want := "+ 2 tool-managed files (diagnostic: +2 -1) · git reset"
	if got := ReceiptText("/workspace", ReceiptEvidence{Files: files}); got != want {
		t.Fatalf("Git receipt = %q, want %q", got, want)
	}
	for _, unavailable := range []string{"binary", "incomplete"} {
		t.Run(unavailable, func(t *testing.T) {
			file := files[0]
			if unavailable == "binary" {
				file.Binary = true
			} else {
				file.Incomplete = "unreadable target"
			}
			if got := ReceiptText("/workspace", ReceiptEvidence{Files: []diffmodel.ReviewFile{file}}); got != "+ 1 tool-managed files (diagnostic: counts unavailable) · git reset" {
				t.Fatalf("unavailable counts = %q", got)
			}
		})
	}
}
