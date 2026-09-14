package router

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestLiveDiffCaptureOrderSurvivesReceiptsAndRestart(t *testing.T) {
	workspace := t.TempDir()
	directory := t.TempDir()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	var histories []mekugiHistory
	for i, edit := range []struct{ thread, before, after string }{
		{"root", "original\n", "first\n"},
		{"child", "first\n", "second\n"},
		{"root", "second\n", "final\n"},
	} {
		call := strconv.Itoa(i)
		if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte(edit.before), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := mekugi.TranslateForHostAt(t.Context(), workspace,
			"in file.txt\ntype "+strconv.Quote(edit.before)+" "+strconv.Quote(edit.after), "")
		if err != nil {
			t.Fatal(err)
		}
		id, err := store.reserveChange(t.Context(), workspace, edit.thread, call)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{ChangeID: id, CorrelationID: call, Attempt: 1, Report: result.Report, ReviewFiles: result.ReviewFiles}
		histories = append(histories, history)
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
			t.Fatal(err)
		}
	}
	var view liveDiffView
	// Receipts arrive backwards, including a refresh before the predecessor is
	// confirmed. A fresh store and idempotent publication must retain the order.
	for _, i := range []int{2, 1, 0} {
		store, err = openMekugiReplayStore(directory)
		if err != nil {
			t.Fatal(err)
		}
		history := histories[i]
		history.confirmed = true
		call := strconv.Itoa(i)
		if err := store.confirmChanges(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
			t.Fatal(err)
		}
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
			t.Fatal(err)
		}
		liveDiffRefreshTest(t, store, workspace, &view)
	}
	check := func(v *liveDiffView) {
		t.Helper()
		liveDiffRefreshTest(t, &mekugiReplayStore{directory: directory}, workspace, v)
		if len(v.files) != 1 {
			t.Fatalf("file membership changed: %#v", v.files)
		}
		file := v.visible[v.files[0].key()]
		if len(file.chunks) != 1 || file.chunks[0].status != "" ||
			!strings.Contains(file.chunks[0].review.Diff, "-original\n+final\n") {
			t.Fatalf("not the combined final result: %#v", file)
		}
		orders := make([]uint64, 0, 3)
		for _, chunk := range v.files[0].chunks {
			orders = append(orders, chunk.captureOrder)
		}
		if !slices.Equal(orders, []uint64{1, 2, 3}) {
			t.Fatalf("capture order changed: %v", orders)
		}
	}
	check(&view)
	check(&liveDiffView{})
	view.flush(true)
	liveDiffScopeCapture(t, store, workspace, "child", "partial-revert",
		filepath.Join(workspace, "file.txt"), "final", "first")
	liveDiffRefreshTest(t, store, workspace, &view)
	result := view.visible[view.files[0].key()]
	if len(result.chunks) != 1 || !strings.Contains(result.chunks[0].review.Diff, "-original\n+first\n") ||
		!result.highlighted {
		t.Fatalf("partial revert did not revive the original-to-latest result: %#v", result)
	}
	liveDiffScopeCapture(t, store, workspace, "root", "full-revert",
		filepath.Join(workspace, "file.txt"), "first", "original")
	liveDiffRefreshTest(t, store, workspace, &view)
	if len(view.visible[view.files[0].key()].chunks) != 0 {
		t.Fatal("full revert retained a combined diff")
	}
}

func TestCaptureOrderCounterFailuresDoNotPublish(t *testing.T) {
	for _, content := range [][]byte{[]byte("invalid"), {255, 255, 255, 255, 255, 255, 255, 255}} {
		t.Run(strconv.Itoa(len(content)), func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			id, err := store.reserveChange(t.Context(), "/workspace", "thread", "call")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.directory, "capture-order"), content, 0600); err != nil {
				t.Fatal(err)
			}
			history := mekugiHistory{ChangeID: id, CorrelationID: "call", ReviewFiles: []mekugi.ReviewFile{{Diff: "diff"}}}
			if err := store.put(t.Context(), "/workspace", map[string]mekugiHistory{"call": history}); err == nil {
				t.Fatal("invalid counter allowed capture publication")
			}
			if _, found, err := store.read("/workspace", "call", false); err != nil || found {
				t.Fatalf("failed reservation published a record: found=%t err=%v", found, err)
			}
		})
	}
}
