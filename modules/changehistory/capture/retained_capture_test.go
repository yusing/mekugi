package capture

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/modules/changehistory/evidence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestBoundedCommandCaptureDoesNotWaitForPublication(t *testing.T) {
	service, err := history.NewService(evidence.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := service.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := &evidence.Source{Store: store, Thread: "bounded-command"}
	workspace := t.TempDir()
	value := "package fixture\n" + strings.Repeat("// retained baseline\n", 2000)
	var names []string
	for i := range 6 {
		name := fmt.Sprintf("file%d.go", i)
		names = append(names, name)
		writeTestFile(t, filepath.Join(workspace, name), value)
	}
	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- store.Transaction(t.Context(), nil, func(*history.Transaction) error {
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	// A sibling publication must not consume the filesystem acquisition hold
	// when all six baselines fit the existing resident-text allowance.
	command := "gofmt -w " + strings.Join(names, " ") + "; go test ./..."
	observation, observed := Capture(t.Context(), []commandanalysis.Command{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false, Options{Directory: workspace, Evidence: source})
	var inputs []string
	for _, name := range names[:2] {
		inputs = append(inputs, "*** Begin Patch\n*** Update File: "+name+"\n@@\n-package fixture\n+package edited\n*** End Patch")
	}
	patches := Patches(inputs, workspace, source)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !observed || observation == nil || observation.Reason == "capture deadline" || len(observation.Files) != len(names) {
		t.Fatalf("bounded capture lost baselines behind publication: %+v", observation)
	}
	for _, file := range observation.Files {
		if file.Error != "" || file.Content != value {
			t.Fatalf("baseline unavailable: %s: %s", file.Path, file.Error)
		}
	}
	if len(patches) != len(inputs) {
		t.Fatalf("patch capture lost inputs: %d", len(patches))
	}
	for _, patch := range patches {
		if len(patch.Files) != 1 || patch.Files[0].Error != "" || patch.Files[0].Before != value {
			t.Fatalf("patch baseline blocked on publication: %+v", patch)
		}
	}
	// Publication still converts resident acquisition text to durable chunks.
	var root string
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		var err error
		root, err = evidence.Write(tx, source, evidence.Snapshot{Files: observation.Files})
		return err
	}); err != nil || root == "" {
		t.Fatalf("publish acquired baselines: %q, %v", root, err)
	}
	cold := &evidence.Source{Read: store.ReadRetained}
	manifest, err := evidence.Open(cold, root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := evidence.ReadFiles(cold, manifest.Files)
	if err != nil || len(files) != len(names) {
		t.Fatalf("cold baseline recovery: files=%d, %v", len(files), err)
	}
	for _, file := range files {
		writeTestFile(t, file.Path, "unrelated live contents\n")
		if text, err := file.Text(); err != nil || text != value || file.ContentRef == nil {
			t.Fatalf("cold baseline is not retained chunked evidence: %s, %v", file.Path, err)
		}
	}
}

func TestRetainedPatchBatchPreservesLargeBaselines(t *testing.T) {
	for _, test := range []struct {
		name        string
		count, size int
	}{{"combined-over-envelope", 4, 7 << 20}, {"shared-inline-budget", 280, evidence.InlineBytes}} {
		t.Run(test.name, func(t *testing.T) {
			service, err := history.NewService(evidence.RecordKinds())
			if err != nil {
				t.Fatal(err)
			}
			store, err := service.Open(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := &evidence.Source{Store: store, Thread: "patches"}
			workspace := t.TempDir()
			var inputs []string
			value := strings.Repeat("x", test.size)
			for i := range test.count {
				name := fmt.Sprintf("file-%03d", i)
				writeTestFile(t, filepath.Join(workspace, name), value)
				inputs = append(inputs, "*** Begin Patch\n*** Update File: "+name+"\n@@\n-x\n+y\n*** End Patch")
			}
			// Exercise the acquisition implementation without conflating the
			// independent native forwarding deadline with resident-byte bounds.
			patches := patchesWithin(t.Context(), inputs, workspace, source)
			if len(patches) != test.count {
				t.Fatalf("lost patches: %d", len(patches))
			}
			inline, spilled := 0, 0
			for _, patch := range patches {
				if len(patch.Files) != 1 || patch.Files[0].Error != "" {
					t.Fatalf("lost patch baseline: %+v", patch)
				}
				file := patch.Files[0]
				inline += len(file.Before)
				got := file.Before
				if file.BeforeRef != nil {
					spilled++
					got, err = file.BeforeRef.Read()
					if err != nil {
						t.Fatal(err)
					}
				}
				if got != value {
					t.Fatal("retained baseline changed")
				}
			}
			if inline > MaxContentBytes || spilled == 0 {
				t.Fatalf("batch resident text escaped bound: inline=%d spilled=%d", inline, spilled)
			}
		})
	}
}

func TestRetainedCaptureKeepsPerFileFailureAndHealthySibling(t *testing.T) {
	service, err := history.NewService(evidence.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := service.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := &evidence.Source{Store: store, Thread: "per-file"}
	workspace := t.TempDir()
	large, small := filepath.Join(workspace, "large"), filepath.Join(workspace, "small")
	writeTestFile(t, large, strings.Repeat("x", MaxFileBytes+1))
	writeTestFile(t, small, "before\n")
	budget := MaxContentBytes
	before := []File{ReadFileIn(t.Context(), large, &budget, source), ReadFileIn(t.Context(), small, &budget, source)}
	writeTestFile(t, large, "changed oversized baseline\n")
	writeTestFile(t, small, "after\n")
	result := Reconcile(t.Context(), Observation{Files: before}, Comparison{Evidence: source})
	if result.Complete || len(result.Files) != 2 {
		t.Fatalf("per-file failure hid sibling: complete=%v files=%d", result.Complete, len(result.Files))
	}
	failed, healthy := false, false
	for _, review := range result.Files {
		file, err := review.Load()
		if err != nil {
			t.Fatal(err)
		}
		failed = failed || file.BeforePath == large && strings.Contains(file.Incomplete, "8 MiB")
		healthy = healthy || file.AfterPath == small && strings.Contains(file.Diff, "+after\n") && file.Incomplete == ""
	}
	if !failed || !healthy {
		t.Fatalf("failure/healthy evidence missing: failed=%v healthy=%v", failed, healthy)
	}
}
