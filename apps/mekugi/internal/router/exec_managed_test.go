package router

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestManagedExecMChangesSurface(t *testing.T) {
	f := newMChangesSliceFixture(t, "managed-history")
	managed := diffmodel.RenderReviewFile("", "managed.txt", "", "generated body\n")
	managed.Origin = "generator"
	id := saveManagedExecChange(t, f.store, f.ctx, f.workspace, f.thread, "managed", []diffmodel.ReviewFile{managed})
	direct := diffmodel.RenderReviewFile("", "direct.txt", "", "authored body\n")
	mixed := saveManagedExecChange(t, f.store, f.ctx, f.workspace, f.thread, "mixed", []diffmodel.ReviewFile{managed, direct})
	list, err := f.store.changes.Read(f.ctx, f.store.scoped(f.ctx).changeScope(), changes.Query{Workspace: f.workspace, IDs: []string{id, mixed}, View: "list"})
	if err != nil || list != mixed+" +1 -0\n" {
		t.Fatalf("authored list: %q, %v", list, err)
	}
	for _, mode := range []string{"", "summary", "net"} {
		text, err := f.store.changes.Read(f.ctx, f.store.scoped(f.ctx).changeScope(), changes.Query{Workspace: f.workspace, IDs: []string{id, mixed}, View: mode})
		if err != nil || strings.Contains(text, "generated") || strings.Contains(text, "managed") || !strings.Contains(text, "direct.txt") {
			t.Fatalf("managed noise in %s: %q, %v", mode, text, err)
		}
	}
	text, err := f.store.changes.Read(f.ctx, f.store.scoped(f.ctx).changeScope(), changes.Query{Workspace: f.workspace, IDs: []string{id}, View: "history"})
	if err != nil || !strings.Contains(text, "generated body") {
		t.Fatalf("diagnostic history lost: %q, %v", text, err)
	}
	data, err := f.store.liveDiffSnapshot(f.ctx, previewmodel.Scope{Workspaces: map[string]map[string]bool{f.workspace: {f.thread: true}}})
	if err != nil || len(data.Publication.Files) != 1 || !strings.HasSuffix(data.Publication.Files[0].Path, "direct.txt") {
		t.Fatalf("managed saved Diff: %+v, %v", data.Publication.Files, err)
	}
}

func saveManagedExecChange(
	t *testing.T,
	store *mekugiReplayStore,
	ctx context.Context,
	workspace, thread, correlation string,
	files []diffmodel.ReviewFile,
) string {
	t.Helper()
	id, err := store.changes.Reserve(ctx, store.scoped(ctx).changeScope(), workspace, thread, correlation)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{correlation: {
		ToolName: nativeExecCommandToolName, Script: "python generate.py", Root: workspace,
		ExecutingThread: thread, CorrelationID: correlation, ChangeID: id, ReviewFiles: files, ExecOutcome: &changecapture.Outcome{
			Status: changecapture.StatusCompleted, Class: "generator", Labels: []string{"formatter"}, Coverage: changecapture.CoverageExact,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestManagedExecReceiptGroupsFiles(t *testing.T) {
	workspace := t.TempDir()
	files := make([]diffmodel.ReviewFile, 5)
	for i := range files {
		path := fmt.Sprintf("generated-%02d.txt", i+1)
		files[i] = diffmodel.RenderReviewFile("", path, "", "content\n")
		files[i].Origin = "generator"
	}
	got := editReceiptText(workspace, mekugiHistory{ReviewFiles: files})
	want := ""
	if got != want {
		t.Fatalf("grouped receipt = %q, want %q", got, want)
	}
}

func TestExecReceiptOmitsSharedOriginNamedByLabels(t *testing.T) {
	workspace := t.TempDir()
	file := diffmodel.RenderReviewFile("a.go", "a.go", "package a\n\nfunc A() {}\n", "package a\n")
	file.OriginNote = "goimports" + changecapture.SharedOriginSuffix
	for _, test := range []struct {
		labels []string
		note   bool
	}{
		{labels: []string{"python3", "goimports"}},
		{labels: []string{"python3"}, note: true},
		{note: true},
	} {
		got := editReceiptText(workspace, mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{file}, ExecOutcome: &changecapture.Outcome{Labels: test.labels}})
		if strings.Contains(got, "(goimports also ran)") != test.note {
			t.Errorf("labels %q receipt = %q, want note %v", test.labels, got, test.note)
		}
	}
}
