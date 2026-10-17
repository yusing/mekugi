package router

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"
)

func TestLiveDiffRelativeDisplayKeepsSourceAndCapture(t *testing.T) {
	workspace := t.TempDir()
	before := filepath.Join(workspace, "old name.txt")
	after := filepath.Join(workspace, "sub", "new.txt")
	source := "const path = " + strconv.Quote(before)
	diff := "--- " + strconv.Quote(before) + "\n+++ " + strconv.Quote(after) + "\n@@ -1 +1 @@\n-old\n+" + source + "\n"
	chunk := presentation.Chunk{
		Review: diffmodel.ReviewFile{BeforePath: before, AfterPath: after, Diff: diff},
	}
	render, err := new(changeview.Renderer).Render(t.Context(), diffrender.TerminalTheme, []presentation.File{{Path: after, Chunks: []presentation.Chunk{chunk}}}, workspace, 240)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.Lines, "\n"))
	if !strings.Contains(text, "old name.txt → sub/new.txt") || !strings.Contains(text, source) || chunk.Review.Diff != diff {
		t.Fatalf("incorrect display or mutated capture: %q", text)
	}
}

func TestLiveDiffMergePreservesFileAndPosition(t *testing.T) {
	var vPublication presentation.Publication
	v := changeview.View{Scroll: map[string]int{"old": 40}}
	vPublication = vPublication.With([]presentation.File{
		{Path: "b", Chunks: []presentation.Chunk{{Key: "old", Status: "prepared"}}},
		{Path: "c"},
	})
	v.Update(vPublication)
	vPublication = vPublication.With([]presentation.File{
		{Path: "c"},
		{Path: "a"},
		{Path: "b", Chunks: []presentation.Chunk{{Key: "new"}, {Key: "old"}}},
	})
	v.Update(vPublication)
	if v.Files[v.Selected].Path != "b" || v.Scroll["old"] != 40 ||
		v.Files[0].Chunks[0].Key != "old" ||
		v.Files[0].Chunks[1].Key != "new" {
		t.Fatalf("viewport shifted: %#v", v)
	}
	v.Selected = 1
	vPublication = vPublication.With(append(slices.Clone(v.Files), presentation.File{Path: "d"}))
	v.Update(vPublication)
	if v.Files[v.Selected].Path != "c" || v.Scroll["old"] != 40 {
		t.Fatal("other-file update stole navigation")
	}
}

func liveDiffTestChange(t *testing.T, store *mekugiReplayStore, workspace, call, path string) {
	t.Helper()
	id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, "thread", call)
	if err != nil {
		t.Fatal(err)
	}
	h := mekugiHistory{
		ChangeID: id, CorrelationID: call,
		ReviewFiles: []diffmodel.ReviewFile{{
			BeforePath: path, AfterPath: path,
			Diff: "update\n--- \"" + path + "\"\n+++ \"" + path + "\"\n@@ -1,80 +1,80 @@\n" + strings.Repeat("-old\n+new\n", 80),
		}},
	}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: h}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveDiffStoreRestartAndMissingEvidence(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffTestChange(t, store, workspace, "one", "x.go")
	reopened := testReplayStore(mekugiReplayStore{directory: store.directory})
	files, err := reopened.liveDiffSnapshotFiles(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: nil}})
	if err != nil || len(files) != 1 || len(files[0].Chunks) != 1 || files[0].Chunks[0].Status != "" {
		t.Fatalf("Files: %#v, %v", files, err)
	}
	if err := os.Rename(filepath.Join(store.directory, legacycall.Name(workspace, "one", false)), filepath.Join(store.directory, "removed-record")); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.liveDiffSnapshotFiles(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: nil}}); err == nil {
		t.Fatal("missing evidence treated as an empty view")
	}
}

func TestLiveDiffLegacyChangeFlagsDoNotAffectSavedEdits(t *testing.T) {
	var viewPublication presentation.Publication
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffTestChange(t, store, workspace, "one", "x.go")
	indices, err := filepath.Glob(filepath.Join(store.directory, "changes-v3-*.json"))
	if err != nil || len(indices) != 1 {
		t.Fatalf("legacy index fixture: %v, %v", indices, err)
	}
	for _, fixture := range []struct{ path, before, after string }{
		{filepath.Join(store.directory, legacycall.Name(workspace, "one", false)), `"History":{`, `"History":{"Applied":false,"ExecObservation":{"Sweep":true,"Class":"scoped","Commands":[{"Command":"python3 edit.py"}]},`},
		{indices[0], `"ID":"one"`, `"Confirmed":false,"ID":"one"`},
	} {
		data, err := os.ReadFile(fixture.path)
		if err != nil {
			t.Fatal(err)
		}
		legacy := strings.Replace(string(data), fixture.before, fixture.after, 1)
		if legacy == string(data) {
			t.Fatalf("legacy fixture missing %q", fixture.before)
		}
		if err := os.WriteFile(fixture.path, []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reopened := testReplayStore(mekugiReplayStore{directory: store.directory})
	record, found, err := reopened.read(workspace, "one", false)
	if err != nil || !found || record.History.ExecObservation == nil || record.History.ExecObservation.Class != "scoped" || len(record.History.ExecObservation.Commands) != 1 {
		t.Fatalf("legacy capture scope lost: %+v found=%v err=%v", record.History.ExecObservation, found, err)
	}
	files, err := reopened.liveDiffSnapshotFiles(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: nil}})
	if err != nil {
		t.Fatal(err)
	}
	view := changeview.View{}
	viewPublication = viewPublication.With(files)
	view.Update(viewPublication)
	view.RefreshVisible()
	if len(view.Files) != 1 {
		t.Fatalf("legacy files: %+v", view.Files)
	}
	visible := view.Visible[view.Files[0].Key()]
	if len(visible.Chunks) != 1 || visible.Chunks[0].Status != "" || !strings.Contains(visible.Chunks[0].Review.Diff, "-old\n+new\n") {
		t.Fatalf("legacy flags changed applied diff: %+v", visible)
	}
}

func TestLiveDiffNativeRenderer(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	workspace := t.TempDir()
	path := filepath.Join(workspace, "x.go")
	file := presentation.File{Path: path, Chunks: []presentation.Chunk{{
		Review: diffmodel.ReviewFile{BeforePath: path, AfterPath: path, Diff: "--- " + strconv.Quote(path) + "\n+++ " + strconv.Quote(path) + "\n@@ -1 +1 @@\n-old\n+new\n"},
	}}}
	render, err := new(changeview.Renderer).Render(t.Context(), diffrender.TerminalTheme, []presentation.File{file}, workspace, 80)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(render.Lines, "\n")
	if strings.Contains(text, workspace) || !strings.Contains(ansi.Strip(text), "x.go") {
		t.Fatalf("native renderer did not render relative paths: %q", text)
	}
	if !strings.Contains(ansi.Strip(text), "new") || !strings.Contains(text, "\x1b[") {
		t.Fatalf("native renderer did not Render: %q", text)
	}
}

func TestLiveDiffOutputBound(t *testing.T) {
	var out diffrender.Output
	out.Grow(changes.MaxReadBytes)
	out.WriteString(strings.Repeat("x", changes.MaxReadBytes))
	if _, err := out.WriteString("overflow"); err == nil {
		t.Fatal("unbounded renderer output")
	}
}

func TestLiveDiffDoesNotInferPrivateScopeFromScriptText(t *testing.T) {
	t.Setenv("GIT_PAGER", "cat")
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffTestChange(t, store, workspace, "workspace", "public.go")
	id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, "thread", "private")
	if err != nil {
		t.Fatal(err)
	}
	h := mekugiHistory{ChangeID: id, CorrelationID: "private", Script: "in @shell/private\n",
		ReviewFiles: []diffmodel.ReviewFile{{AfterPath: "private-script", Diff: "private-content\n"}},
	}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"private": h}); err != nil {
		t.Fatal(err)
	}
	files, err := store.liveDiffSnapshotFiles(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: nil}})
	if err != nil || len(files) != 2 {
		t.Fatalf("script spelling hid workspace evidence: %#v %v", files, err)
	}
}

func liveDiffIdentityCapture(t *testing.T, store *mekugiReplayStore, workspace, call, from, to, before, after string) {
	t.Helper()
	id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, "identity-thread", call)
	if err != nil {
		t.Fatal(err)
	}
	header := func(path string) string {
		if path == "" {
			return "/dev/null"
		}
		return strconv.Quote(path)
	}
	diff := "move " + strconv.Quote(from) + " -> " + strconv.Quote(to) + "\n"
	if before != after {
		oldRange, newRange := "1,1", "1,1"
		if before == "" {
			oldRange = "0,0"
		}
		if after == "" {
			newRange = "0,0"
		}
		diff = "--- " + header(from) + "\n+++ " + header(to) + "\n@@ -" + oldRange + " +" + newRange + " @@\n"
		if before != "" {
			diff += "-" + before
		}
		if after != "" {
			diff += "+" + after
		}
	}
	h := mekugiHistory{ChangeID: id, CorrelationID: call, ReviewFiles: []diffmodel.ReviewFile{{BeforePath: from, AfterPath: to, Diff: diff}}}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: h}); err != nil {
		t.Fatal(err)
	}
}

func liveDiffRefreshTest(t *testing.T, store *mekugiReplayStore, workspace string, view *changeview.View) {
	var viewPublication presentation.Publication
	t.Helper()
	files, err := store.liveDiffSnapshotFiles(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: nil}})
	if err != nil {
		t.Fatal(err)
	}
	viewPublication = viewPublication.With(files)
	view.Update(viewPublication)
	view.RefreshVisible()
}

func liveDiffVisibleText(file presentation.File) string {
	var text strings.Builder
	for _, chunk := range file.Chunks {
		text.WriteString(chunk.Status + "\n" + chunk.Review.UnifiedDiff())
	}
	return text.String()
}

func TestLiveDiffSavedMoveCarriesBaselineToDestination(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := changeview.View{Scroll: make(map[string]int)}
	liveDiffIdentityCapture(t, store, workspace, "A", "a.txt", "a.txt", "old\n", "first\n")
	liveDiffRefreshTest(t, store, workspace, &v)
	liveDiffIdentityCapture(t, store, workspace, "move", "a.txt", "b.txt", "first\n", "first\n")
	liveDiffIdentityCapture(t, store, workspace, "fix", "b.txt", "b.txt", "first\n", "fixed\n")
	liveDiffRefreshTest(t, store, workspace, &v)
	if len(v.Files) != 1 {
		t.Fatalf("saved rename split into multiple files: %#v", v.Files)
	}
	text := liveDiffVisibleText(v.Visible[v.Files[0].Key()])
	if v.Files[0].Path != filepath.Join(workspace, "b.txt") ||
		!strings.Contains(text, "-old\n+fixed\n") || strings.Contains(text, "Uncomposed") {
		t.Fatalf("saved move lost destination baseline: %#v %q", v.Files, text)
	}
}

func TestLiveDiffMoveIntoDeletedPathKeepsChainsSeparate(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(strconv.FormatBool(reverse), func(t *testing.T) {
			workspace := t.TempDir()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			v := changeview.View{Scroll: map[string]int{workspace + "\x00A/0": 9}}
			order := []string{"A", "B"}
			if reverse {
				slices.Reverse(order)
			}
			for _, id := range order {
				path := strings.ToLower(id) + ".txt"
				liveDiffIdentityCapture(t, store, workspace, id, path, path, "old"+id+"\n", id+"\n")
			}
			liveDiffRefreshTest(t, store, workspace, &v)
			v.Selected = slices.IndexFunc(v.Files, func(f presentation.File) bool { return f.Key() == workspace+"\x00A/0" })
			liveDiffIdentityCapture(t, store, workspace, "deleteB", "b.txt", "", "B\n", "")
			liveDiffRefreshTest(t, store, workspace, &v)
			liveDiffIdentityCapture(t, store, workspace, "moveA", "a.txt", "b.txt", "A\n", "A\n")
			liveDiffRefreshTest(t, store, workspace, &v)
			liveDiffIdentityCapture(t, store, workspace, "fixA", "b.txt", "b.txt", "A\n", "fixed\n")
			liveDiffRefreshTest(t, store, workspace, &v)
			a, b := liveDiffVisibleText(v.Visible[workspace+"\x00A/0"]), liveDiffVisibleText(v.Visible[workspace+"\x00B/0"])
			if len(v.Files) != 2 || v.Files[v.Selected].Key() != workspace+"\x00A/0" || v.Scroll[workspace+"\x00A/0"] != 9 ||
				!strings.Contains(a, "-oldA\n+fixed\n") || !strings.Contains(b, "-oldB\n") || strings.Contains(b, "+fixed\n") ||
				strings.Contains(a+b, "Uncomposed") {
				t.Fatalf("path reuse conflated chains: %#v a=%q b=%q", v.Files, a, b)
			}
		})
	}
}
