package router

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestMChangesCompactViewsKeepKnownStatsWithoutManagedNoise(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	ctx, release, err := store.beginSession(t.Context(), "author", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	id, err := store.reserveChange(ctx, workspace, "author", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	direct := mekugi.RenderReviewFile(filepath.Join(workspace, "direct.go"), filepath.Join(workspace, "direct.go"), "old\n", "new\n")
	managed := mekugi.RenderReviewFile("", filepath.Join(workspace, "formatted.go"), "", "package p\n")
	managed.Origin = "gofmt"
	unknown := mekugi.RenderIncompleteReviewFile(filepath.Join(workspace, "unread.go"), filepath.Join(workspace, "unread.go"), "capture deadline")
	unknown.Origin = "go test"
	history := mekugiHistory{ChangeID: id, CorrelationID: "mixed", ExecOutcome: &execOutcome{Status: execStatusUnconfirmed, Class: "scoped", Coverage: execCoveragePartial}, ReviewFiles: []mekugi.ReviewFile{direct, managed, unknown}}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"mixed": history}); err != nil {
		t.Fatal(err)
	}
	list, err := store.readChanges(ctx, changeReadOptions{workspace: workspace, view: "list"})
	if err != nil || list != id+" observed partial +1 -1 managed:2\n" {
		t.Fatalf("compact list: %q %v", list, err)
	}
	summary, err := store.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{id}, view: "summary"})
	if err != nil || summary != "1\t1\tdirect.go\ntool-managed: 1 changed +1 -0; 1 counts unavailable\n" {
		t.Fatalf("compact summary: %q %v", summary, err)
	}
	review, err := store.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{id}})
	if err != nil || !strings.Contains(review, "+new\n") || strings.Contains(review, "+package p\n") {
		t.Fatalf("managed content leaked into default diff: %q %v", review, err)
	}
	named, err := store.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{id}, view: "summary", paths: []string{"formatted.go"}})
	if err != nil || named != "1\t0\ttool-managed\tformatted.go\n" {
		t.Fatalf("explicit managed path unavailable: %q %v", named, err)
	}
}

func TestMChangesListDoesNotMergeSharedWithExclusive(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	ctx, release, err := store.beginSession(t.Context(), "author", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	save := func(call string, overlaps []string) string {
		t.Helper()
		id, err := store.reserveChange(ctx, workspace, "author", call)
		if err != nil {
			t.Fatal(err)
		}
		file := mekugi.RenderReviewFile(call, call, "old\n", "new\n")
		outcome := &execOutcome{Status: execStatusCompleted, Class: "scoped", Coverage: execCoverageExact, Overlaps: overlaps}
		if err := store.put(ctx, workspace, map[string]mekugiHistory{call: {ChangeID: id, CorrelationID: call, ExecOutcome: outcome, ReviewFiles: []mekugi.ReviewFile{file}}}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	shared := save("shared", []string{"other-call"})
	exclusive := save("exclusive", nil)
	list, err := store.readChanges(ctx, changeReadOptions{workspace: workspace, view: "list"})
	if err != nil || list != shared+" completed shared exact +1 -1\n"+exclusive+" completed exact +1 -1\n" {
		t.Fatalf("shared attribution was lost or merged: %q %v", list, err)
	}
}
