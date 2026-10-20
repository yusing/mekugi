package capture

import (
	"path/filepath"
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
