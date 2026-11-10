package evidence

import (
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/activitydetail"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func testSource(t testing.TB) *Source {
	t.Helper()
	service, err := history.NewService(RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := service.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Source{Store: store, Thread: "capture", Writes: new(persistence.Counter)}
}

func saveSnapshot(t testing.TB, source *Source, snapshot Snapshot) string {
	t.Helper()
	var root string
	err := source.Store.Transaction(t.Context(), source.Writes, func(tx *history.Transaction) error {
		var err error
		root, err = Write(tx, source, snapshot)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestChunkedEvidenceColdSelectionAndWriteScaling(t *testing.T) {
	// Each collection exceeds the historical 32 MiB envelope, but acquisition
	// and cold selection retain only one file's text at a time.
	for _, count := range []int{8, 36} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			source := testSource(t)
			var snapshot Snapshot
			bytes := 0
			for i := range count {
				value := fmt.Sprintf("file %d\n", i) + strings.Repeat("retained line\n", 81000)
				text, err := source.SaveText(t.Context(), value)
				if err != nil {
					t.Fatal(err)
				}
				snapshot.Files = append(snapshot.Files, File{Path: fmt.Sprintf("/w/%d", i), ContentRef: text})
				diff := "@@ -0,0 +1 @@\n+" + strings.ReplaceAll(value, "\n", "\n+")
				budget := 0
				snapshot.Reviews = append(snapshot.Reviews, RetainReview(t.Context(), source, diffmodel.ReviewFile{AfterPath: fmt.Sprintf("/w/%d", i), Diff: diff}, &budget))
				bytes += len(value) + len(diff)
			}
			root := saveSnapshot(t, source, snapshot)
			written := source.Writes.Snapshot().Bytes
			if written > uint64(bytes)+uint64(count)*12000 {
				t.Fatalf("evidence re-encoded across its batch: source=%d written=%d", bytes, written)
			}
			textReads := 0
			cold := &Source{Read: func(name string, limit int64) ([]byte, error) {
				if textName(name) {
					textReads++
				}
				return source.Store.ReadRetained(name, limit)
			}}
			loaded, err := Read(cold, root)
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Files) != count || len(loaded.Reviews) != count || textReads != 0 {
				t.Fatalf("cold metadata expanded text: files=%d reviews=%d reads=%d", len(loaded.Files), len(loaded.Reviews), textReads)
			}
			selected, err := LoadReviews(loaded.Reviews, func(file diffmodel.ReviewFile) bool { return file.AfterPath == "/w/0" }, MaxTextBytes)
			if err != nil || len(selected) != 1 || !strings.Contains(selected[0].Diff, "+file 0\n") {
				t.Fatalf("selected evidence: files=%d error=%v", len(selected), err)
			}
			if textReads != len(loaded.Reviews[0].Text.Parts) {
				t.Fatalf("selected read expanded siblings: %d", textReads)
			}
			t.Logf("files=%d source_bytes=%d written_bytes=%d cold_text_reads=%d", count, bytes, written, textReads)
		})
	}
}

func TestStagedTextProtectsReusedExpiredChunks(t *testing.T) {
	source := testSource(t)
	value := strings.Repeat("old shared text\n", 80000)
	text, err := source.SaveText(t.Context(), value)
	if err != nil {
		t.Fatal(err)
	}
	saveSnapshot(t, source, Snapshot{Files: []File{{Path: "/w/file", ContentRef: text}}})
	old := time.Now().Add(-history.SessionRetention - time.Hour)
	catalog, err := source.Store.ReadCatalog(history.SessionName(source.Thread))
	if err != nil {
		t.Fatal(err)
	}
	catalog.LastUsed = old
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.Write(history.SessionName(source.Thread), "catalog-pending-", data)
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source.Store.Directory())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "change-") {
			continue
		}
		if err := os.Chtimes(filepath.Join(source.Store.Directory(), entry.Name()), old, old); err != nil {
			t.Fatal(err)
		}
	}
	// A new capture reuses byte-identical chunks owned by an expired thread.
	// Its fresh staging root must pin them before its final manifest exists.
	source.Thread = "new-capture"
	reused, err := source.SaveText(t.Context(), value)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Store.Cleanup(t.Context(), nil, history.Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	got, err := reused.Read()
	if err != nil || got != value {
		t.Fatalf("staged reused chunks expired: %v", err)
	}
	saveSnapshot(t, source, Snapshot{Files: []File{{Path: "/w/file", ContentRef: reused}}})
}

func TestMissingTextRejectsPublicationWithoutManifest(t *testing.T) {
	source := testSource(t)
	text, err := source.SaveText(t.Context(), strings.Repeat("captured\n", 10000))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(source.Store.Directory(), text.Parts[0])); err != nil {
		t.Fatal(err)
	}
	err = source.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		_, err := Write(tx, source, Snapshot{Files: []File{{Path: "/w/file", ContentRef: text}}})
		return err
	})
	if err == nil {
		t.Fatal("published a missing text dependency")
	}
	names, err := filepath.Glob(filepath.Join(source.Store.Directory(), "change-evidence-*.json"))
	if err != nil || len(names) != 0 {
		t.Fatalf("failed publication exposed a manifest: %v, %v", names, err)
	}
}

func TestReadMetadataHasAggregateBound(t *testing.T) {
	source := testSource(t)
	root := saveSnapshot(t, source, Snapshot{Files: []File{{Path: "/w/" + strings.Repeat("x", 600000)}}})
	manifest, err := Open(source, root)
	if err != nil {
		t.Fatal(err)
	}
	refs := make([]string, 100)
	for i := range refs {
		refs[i] = manifest.Files[0]
	}
	if _, err := ReadFiles(source, refs); err == nil || !strings.Contains(err.Error(), "metadata exceeds") {
		t.Fatalf("unbounded metadata accepted: %v", err)
	}
}

func TestRetainedTextAndReceiptValidation(t *testing.T) {
	source := testSource(t)
	text, err := source.SaveText(t.Context(), strings.Repeat("雪", 20000))
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*Text){func(v *Text) { v.Size = MaxTextBytes + 1 }, func(v *Text) { v.Digest = "not-a-digest" }, func(v *Text) { v.Parts = []string{"../escape"} }} {
		invalid := *text
		edit(&invalid)
		if invalid.Bind(source) == nil {
			t.Fatal("invalid retained text reference accepted")
		}
	}
	files, _, truncated, err := ReceiptFiles([]Review{Inline(diffmodel.ReviewFile{Diff: "雪雪"}), {Text: text}}, 4)
	if err != nil || files[0].Diff != "雪" || files[1].Diff != "" || !truncated[0] || !truncated[1] {
		t.Fatalf("receipt split UTF-8: %+v, %v", files, err)
	}
	if err := os.WriteFile(filepath.Join(source.Store.Directory(), text.Parts[0]), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := text.Read(); err == nil {
		t.Fatal("corrupt retained text accepted")
	}
	if err := text.Verify(); err == nil {
		t.Fatal("count-only verification accepted corrupt text")
	}
	budget := 0
	failed := RetainReview(t.Context(), &Source{}, diffmodel.ReviewFile{BeforePath: "/w/file", AfterPath: "/w/file", Diff: strings.Repeat("x", InlineBytes+1), Origin: "git reset", CopyFrom: "/w/source"}, &budget)
	if failed.Incomplete == "" || failed.Diff != "" || failed.Origin != "git reset" || failed.CopyFrom != "/w/source" {
		t.Fatalf("retention failure changed provenance: %+v", failed)
	}
}

func TestCaptureSourceAnchorRoundTrip(t *testing.T) {
	source := testSource(t)
	want := activitydetail.SourceAnchor{Call: "formatter", Segment: 2}
	root := saveSnapshot(t, source, Snapshot{SourceAnchor: want})
	cold := &Source{Store: source.Store, Thread: source.Thread}
	restored, err := Read(cold, root)
	if err != nil || restored.SourceAnchor != want {
		t.Fatalf("source anchor=%+v err=%v", restored.SourceAnchor, err)
	}
	for _, anchor := range []activitydetail.SourceAnchor{{Call: "writer"}, {Segment: 1}, {Call: "writer", Segment: -1}} {
		err := source.Store.Transaction(t.Context(), source.Writes, func(tx *history.Transaction) error {
			_, err := Write(tx, source, Snapshot{SourceAnchor: anchor})
			return err
		})
		if err == nil {
			t.Fatalf("invalid source anchor accepted: %+v", anchor)
		}
	}
}
