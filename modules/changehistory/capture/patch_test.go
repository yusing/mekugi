package capture

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNativePatchPathsIgnoresHunkContentThatLooksLikeAnOperation(t *testing.T) {
	workspace := t.TempDir()
	patch := "*** Begin Patch\n*** Update File: real.txt\n@@\n-*** Add File: fake.txt\n+replacement\n*** End Patch\n"
	paths, err := nativePatchPaths(patch, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0].before != filepath.Join(workspace, "real.txt") || paths[0].after != paths[0].before {
		t.Fatalf("observed paths = %+v", paths)
	}
}

func TestPatchBoundaryRepeatedTargetsShareBudget(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "value.txt")
	patch := "*** Begin Patch\n" + strings.Repeat("*** Update File: value.txt\n@@\n-before\n+after\n", 3) + "*** End Patch\n"
	observation, err := PatchBoundary(patch, workspace, []File{{Path: path, Kind: Text, Content: strings.Repeat("x", 5<<20)}})
	if err != nil || len(observation.Files) != 3 {
		t.Fatalf("boundary: %v %+v", err, observation)
	}
	total := 0
	for i, file := range observation.Files {
		total += len(file.Before)
		if i > 0 && file.Error == "" {
			t.Fatal("duplicate target escaped aggregate bound")
		}
	}
	if total > MaxContentBytes {
		t.Fatalf("retained %d bytes", total)
	}
}
