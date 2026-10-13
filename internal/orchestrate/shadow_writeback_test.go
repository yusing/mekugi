package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShadowMatchesGitExecutableMode(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, tc := range []struct {
		name          string
		snapshot, now os.FileMode
		mismatch      bool
	}{
		{"group-only", 0654, 0654, false},
		{"other-only", 0645, 0645, false},
		{"owner-only", 0744, 0744, false},
		{"unrelated-bits", 0654, 0621, false},
		{"missing-owner-group-remains", 0754, 0654, true},
		{"missing-owner-other-remains", 0745, 0645, true},
		{"added-owner", 0644, 0744, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := t.TempDir()
			file := filepath.Join(source, "file")
			for _, err := range []error{os.WriteFile(file, []byte("content"), 0600), os.Chmod(file, tc.snapshot), os.Symlink("file", filepath.Join(source, "link"))} {
				if err != nil {
					t.Fatal(err)
				}
			}
			run := func(args ...string) string {
				t.Helper()
				out, err := git(t.Context(), source, args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			run("init", "-q")
			// The snapshot owner also uses Git add to normalize filesystem modes.
			run("-c", "core.filemode=true", "add", "file", "link")
			repo := filepath.Join(source, ".git")
			entries, err := shadowEntries(t.Context(), repo, run("write-tree"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(file, tc.now); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(source)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			// This comparison guards both preimages and uncertain-write postimages.
			err = shadowMatches(t.Context(), root, repo, "file", entries["file"], entries)
			if (err != nil) != tc.mismatch || err != nil && !strings.Contains(err.Error(), "source executable state changed") {
				t.Fatalf("snapshot %04o, source %04o: %v", tc.snapshot, tc.now, err)
			}
			if err := shadowMatches(t.Context(), root, repo, "link", entries["link"], entries); err != nil {
				t.Fatalf("symlink: %v", err)
			}
		})
	}
}
