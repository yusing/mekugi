package router

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

type mountedJournalDiscoveryFixture struct {
	store    *mekugiReplayStore
	journals *journalStore
	run      journalRun
	batches  []orchestrate.Batch
	records  int
}

// Retained facts only: no checkout creation, host processes, live ancestry, or
// session-retention writes. All measured operations reopen real journal files.
func newMountedJournalDiscoveryFixture(tb testing.TB, unrelated int) mountedJournalDiscoveryFixture {
	tb.Helper()
	root := tb.TempDir()
	f := mountedJournalDiscoveryFixture{
		store:    &mekugiReplayStore{directory: filepath.Join(root, "replay")},
		journals: newJournalStore(),
		run:      journalRun{Directory: filepath.Join(root, "runs"), Workspace: filepath.Join(root, "main"), Main: "main"},
	}
	if err := os.MkdirAll(f.store.directory, 0700); err != nil {
		tb.Fatal(err)
	}
	main := f.record(f.run.Workspace, "main", "", "/root")
	main.Orchestration = &f.run
	for i := range 2 {
		name := fmt.Sprintf("batch_%d", i)
		workspace := filepath.Join(root, name)
		f.batches = append(f.batches, orchestrate.Batch{TaskName: name, Cwd: workspace, State: "prepared", Launch: &orchestrate.Launch{ThreadID: name}})
		child := f.record(workspace, name, "", "/root")
		child.Orchestration = &f.run
		f.write(tb, child)
		f.write(tb, f.record(workspace, name+"_worker", name, "/root/worker"))
		main.Items[i].Agent = "/root/" + name
	}
	f.write(tb, main)
	// Use the manifest owner's location selection, then retain a bounded run
	// snapshot in that file. Snapshot performs no checkout or dispatch effects.
	if err := (&orchestrate.Store{Directory: f.run.Directory}).Start(tb.Context(), f.run.Workspace, f.run.Main); err != nil {
		tb.Fatal(err)
	}
	entries, err := os.ReadDir(f.run.Directory)
	if err != nil {
		tb.Fatal(err)
	}
	manifest := struct {
		Version   int                 `json:"version"`
		Workspace string              `json:"workspace"`
		Main      string              `json:"main"`
		Batches   []orchestrate.Batch `json:"batches"`
	}{1, f.run.Workspace, f.run.Main, f.batches}
	data, err := marshalProtocolJSON(manifest)
	if err != nil {
		tb.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			if err := os.WriteFile(filepath.Join(f.run.Directory, entry.Name()), data, 0600); err != nil {
				tb.Fatal(err)
			}
		}
	}
	for i := range unrelated {
		workspace := filepath.Join(root, fmt.Sprintf("unrelated_%d", i))
		for j := range 4 {
			f.write(tb, f.record(workspace, fmt.Sprintf("unrelated_%d_%d", i, j), "", "/root"))
		}
	}
	f.records = 5 + unrelated*4
	return f
}

func (f mountedJournalDiscoveryFixture) record(workspace, thread, parent, author string) threadJournal {
	j := threadJournal{Version: 2, Workspace: workspace, Thread: thread, Parent: parent, Author: author,
		IdentityKnown: true, TreeAuthored: true, Receipts: make(map[string]journalReceipt)}
	for i := range 32 {
		j.Items = append(j.Items, journalItem{Path: fmt.Sprintf("/%d", i+1), Kind: "task", Title: "Retained assignment", State: "working",
			Author: author, Body: strings.Repeat("Retained measurement payload. ", 8)})
	}
	return j
}

func (f mountedJournalDiscoveryFixture) write(tb testing.TB, j threadJournal) {
	tb.Helper()
	data, err := marshalProtocolJSON(j)
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.store.directory, journalFilename(j.Workspace, j.Thread)), data, 0600); err != nil {
		tb.Fatal(err)
	}
}

func TestMountedJournalDiscoveryRetainedRun(t *testing.T) {
	for _, identity := range []string{"valid", "missing", "conflicted", "corrupt_contents"} {
		t.Run(identity, func(t *testing.T) {
			f := newMountedJournalDiscoveryFixture(t, 8)
			batch := f.batches[0]
			child, _, err := readThreadJournal(f.store, batch.Cwd, batch.Launch.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			switch identity {
			case "missing":
				if err := os.Remove(filepath.Join(f.store.directory, journalFilename(child.Workspace, child.Thread))); err != nil {
					t.Fatal(err)
				}
			case "conflicted":
				child.IdentityConflicted = true
				f.write(t, child)
			case "corrupt_contents":
				child.Items = append(child.Items, make([]journalItem, maxJournalItems)...)
				f.write(t, child)
			}
			nodes, err := f.journals.readTree(t.Context(), f.store, f.run.Workspace, f.run.Main, "", "", nil, "combined")
			if identity == "corrupt_contents" {
				if err == nil {
					t.Fatal("identifiable corrupt member appeared as a successful combined read")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, mounted := mountFind(nodes, "/1/@batch_0/1")
			_, worker := mountFind(nodes, "/1/@batch_0/@agents/@batch_0_worker/1")
			if mounted != (identity == "valid") || worker != mounted {
				t.Fatalf("root/descendant admission: identity=%s mounted=%v worker=%v", identity, mounted, worker)
			}
			if _, pending := mountFind(nodes, "/1/@pending"); pending != (identity != "valid") {
				t.Fatalf("unresolved identity lost pending binding: %s", identity)
			}
		})
	}
}

func BenchmarkMountedJournalDiscovery(b *testing.B) {
	for _, unrelated := range []int{0, 32, 128} {
		b.Run(fmt.Sprintf("unrelated_workspaces_%d", unrelated), func(b *testing.B) {
			f := newMountedJournalDiscoveryFixture(b, unrelated)
			for _, operation := range []string{"journalWorkspaces", "relatedJournals", "publishMountedViews", "publishNoMatchingSink", "ownRead"} {
				b.Run(operation, func(b *testing.B) {
					journals := newJournalStore()
					sink := journals.attachNative(f.run.Workspace, f.run.Main)
					thread := f.batches[0].Launch.ThreadID
					if operation == "publishNoMatchingSink" {
						journals.detachNative(sink)
						sink = journals.attachNative(filepath.Join(f.run.Workspace, "other"), "other")
					}
					b.ReportAllocs()
					for b.Loop() {
						switch operation {
						case "journalWorkspaces":
							views, err := journals.journalWorkspaces(f.store)
							if err != nil || len(views) != unrelated+3 {
								b.Fatalf("workspace snapshot: count=%d err=%v", len(views), err)
							}
						case "relatedJournals":
							joined, _, err := journals.relatedJournals(f.store, f.run.Workspace, f.run.Main)
							if err != nil || len(joined) != 5 {
								b.Fatalf("retained run: count=%d err=%v", len(joined), err)
							}
						case "publishMountedViews", "publishNoMatchingSink":
							journals.publishMountedViews(f.store, f.batches[0].Cwd, thread)
						case "ownRead":
							if nodes, err := journals.readTree(b.Context(), f.store, f.run.Workspace, f.run.Main, "", "", nil, "own"); err != nil || len(nodes) != 32 {
								b.Fatalf("own read: count=%d err=%v", len(nodes), err)
							}
						}
					}
					if operation == "publishMountedViews" && (sink.mounted == nil || !slices.ContainsFunc(sink.mounted.Items, func(item journalItem) bool { return item.Path == "/1/@batch_0/1" })) {
						b.Fatal("publication did not reach the coordinator sink")
					}
					if operation == "publishNoMatchingSink" && sink.mounted != nil {
						b.Fatal("unrelated publication changed the coordinator sink")
					}
					b.ReportMetric(float64(f.records), "retained-records")
				})
			}
		})
	}
}
