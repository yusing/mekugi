package presentation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

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
