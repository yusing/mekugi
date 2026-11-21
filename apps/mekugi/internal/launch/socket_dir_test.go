package launch

import (
	"os"
	"path/filepath"
	"testing"
)

// shortTempDir returns a symlink-free test directory under /tmp. Unix socket paths
// beneath it stay within macOS's 104-byte sun_path limit, which t.TempDir paths
// under the per-user temporary directory can exceed.
func shortTempDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mekugi-")
	if err == nil {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		dir, err = filepath.EvalSymlinks(dir)
	}
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
