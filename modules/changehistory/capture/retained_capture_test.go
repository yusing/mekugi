package capture

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/evidence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

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
