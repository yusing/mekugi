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
