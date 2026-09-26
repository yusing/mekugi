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
	history := mekugiHistory{ChangeID: id, CorrelationID: "mixed", ExecOutcome: &execOutcome{Class: "scoped", Coverage: execCoveragePartial}, ReviewFiles: []mekugi.ReviewFile{direct, managed, unknown}}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"mixed": history}); err != nil {
		t.Fatal(err)
	}
	list, err := store.readChanges(ctx, changeReadOptions{workspace: workspace, view: "list"})
	if err != nil || list != id+" +1 -1 managed:2\n" {
		t.Fatalf("compact list: %q %v", list, err)
	}
	summary, err := store.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{id}, view: "summary"})
	if err != nil || summary != "M\t1\t1\tdirect.go\nM +1 -0; 1 counts unavailable\n" {
		t.Fatalf("compact summary: %q %v", summary, err)
	}
	review, err := store.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{id}})
	if err != nil || !strings.Contains(review, "+new\n") || strings.Contains(review, "+package p\n") {
		t.Fatalf("managed content leaked into default diff: %q %v", review, err)
	}
	named, err := store.readChanges(ctx, changeReadOptions{workspace: workspace, ids: []string{id}, view: "summary", paths: []string{"formatted.go"}})
	if err != nil || named != "A\t1\t0\ttool-managed\tformatted.go\n" {
		t.Fatalf("explicit managed path unavailable: %q %v", named, err)
	}
}

func TestMChangesListCompressesAppliedChangesRegardlessOfCommandAttribution(t *testing.T) {
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
	if err != nil || list != shared+".."+exclusive+" +2 -2\n" {
		t.Fatalf("applied change range: %q %v", list, err)
	}
}

func TestMChangesListHidesLegacyNoOpsWithoutRetiringEvidence(t *testing.T) {
	f := newMChangesSliceFixture(t, "legacy-noops")
	var ids []string
	for _, call := range []string{"first", "noop", "last"} {
		id := f.reserve(t, f.thread, call)
		ids = append(ids, id)
		history := mekugiHistory{}
		if call != "noop" {
			history.ReviewFiles = []mekugi.ReviewFile{mekugi.RenderReviewFile("file.txt", "file.txt", "old\n", "new\n")}
		} else {
			history.AlreadySatisfied = true
		}
		f.publish(t, id, call, call, history)
	}
	pending := f.reserve(t, f.thread, "pending")
	stdout, stderr, status := f.run(t, "mchanges --list")
	want := ids[0] + " +1 -1\n" + ids[2] + " +1 -1\n" + pending + " pending\n"
	if status != 0 || stderr != "" || stdout != want {
		t.Fatalf("legacy no-op list = %q, %q, %d; want %q", stdout, stderr, status, want)
	}
	stdout, stderr, status = f.run(t, "mchanges "+ids[1]+" --history")
	if status != 0 || stderr != "" || !strings.Contains(stdout, "noop") {
		t.Fatalf("legacy attempt became unreadable: %q %q %d", stdout, stderr, status)
	}
}

func TestMChangesSummaryFileStatusesMatchDiffPane(t *testing.T) {
	for _, test := range []struct {
		name  string
		files []mekugi.ReviewFile
		later []mekugi.ReviewFile
		want  string
	}{
		{name: "added then edited", files: []mekugi.ReviewFile{mekugi.RenderReviewFile("", "file", "", "a\n"), mekugi.RenderReviewFile("file", "file", "a\n", "b\n")}, want: "A\t2\t1\tfile\n"},
		{name: "created then deleted", files: []mekugi.ReviewFile{mekugi.RenderReviewFile("", "file", "", "a\n")}, later: []mekugi.ReviewFile{mekugi.RenderReviewFile("file", "", "a\n", "")}, want: ""},
		{name: "deleted", files: []mekugi.ReviewFile{mekugi.RenderReviewFile("file", "", "a\n", "")}, want: "D\t0\t1\tfile\n"},
		{name: "modified", files: []mekugi.ReviewFile{mekugi.RenderReviewFile("file", "file", "a\n", "b\n")}, want: "M\t1\t1\tfile\n"},
		{name: "renamed", files: []mekugi.ReviewFile{mekugi.RenderReviewFile("old", "new", "a\n", "a\n")}, want: "R\t0\t0\told => new\n"},
		{name: "renamed and modified", files: []mekugi.ReviewFile{mekugi.RenderReviewFile("old", "new", "a\n", "b\n")}, want: "RM\t1\t1\told => new\n"},
		{name: "rename then edit in later change", files: []mekugi.ReviewFile{mekugi.RenderReviewFile("old", "new", "a\n", "a\n")}, later: []mekugi.ReviewFile{mekugi.RenderReviewFile("new", "new", "a\n", "b\n")}, want: "RM\t1\t1\told => new\n"},
		{name: "rename incomplete", files: []mekugi.ReviewFile{mekugi.RenderIncompleteReviewFile("old", "new", "capture unavailable")}, want: "RM\t-\t-\told => new\n"},
		{name: "binary rename", files: []mekugi.ReviewFile{mekugi.RenderBinaryReviewFile("old", "new", 3, 3, "abc", "abc")}, want: "R\t-\t-\told => new\n"},
		{name: "conflict", files: []mekugi.ReviewFile{mekugi.RenderReviewFile("file", "file", "a\n", "<<<<<<< workspace\na\n=======\nb\n>>>>>>> mchanges revert amber1\n")}, want: "UU\t4\t0\tfile\n"},
		{name: "resolved conflict in later change", files: []mekugi.ReviewFile{mekugi.RenderReviewFile("file", "file", "a\n", "<<<<<<< workspace\na\n=======\nb\n>>>>>>> mchanges revert amber1\n")}, later: []mekugi.ReviewFile{mekugi.RenderReviewFile("file", "file", "<<<<<<< workspace\na\n=======\nb\n>>>>>>> mchanges revert amber1\n", "c\n")}, want: "M\t5\t5\tfile\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			id, err := store.reserveChange(t.Context(), workspace, "author", "edit")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"edit": {ChangeID: id, CorrelationID: "edit", ReviewFiles: test.files}}); err != nil {
				t.Fatal(err)
			}
			ids := []string{id}
			if len(test.later) > 0 {
				laterID, err := store.reserveChange(t.Context(), workspace, "other-author", "later")
				if err != nil {
					t.Fatal(err)
				}
				if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"later": {ChangeID: laterID, CorrelationID: "later", ReviewFiles: test.later}}); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, laterID)
			}
			got, err := store.readChanges(t.Context(), changeReadOptions{workspace: workspace, ids: ids, view: "summary"})
			if err != nil || got != test.want {
				t.Fatalf("summary = %q, %v; want %q", got, err, test.want)
			}
			if len(ids) > 1 {
				got, err = store.readChanges(t.Context(), changeReadOptions{workspace: workspace, ids: []string{ids[1], ids[0]}, view: "summary"})
				if err != nil || got != test.want {
					t.Fatalf("reversed cross-author summary = %q, %v; want %q", got, err, test.want)
				}
			}
		})
	}
}
