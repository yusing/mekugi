package mekugi

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/patchtest"
)

// Native execution is opt-in so ordinary package tests do not depend on Codex.
// Set MEKUGI_TEST_APPLY_PATCH to the installed host's apply_patch executable.
func TestHostNewlineParity(t *testing.T) {
	for _, test := range []struct {
		name, before, edit, want string
	}{
		{"authored literal LF", "old\nnext\n", "type \"old\" <<END\nnew\nEND\n", "new\n\nnext\n"},
		{"insert before blank", "old\n\nnext\n", "add " + row(2, "") + ` "inserted\n"`, "old\ninserted\n\nnext\n"},
		{"literal", "old\nnext\n", `type "old" "new"`, "new\nnext\n"},
		{"row", "old\nnext\n", "type " + row(1, "old") + ` "new\n"`, "new\nnext\n"},
		{"range", "old\nnext\n", "type " + row(1, "old") + ".." + row(2, "next") + ` "new\n"`, "new\n"},
		{"before row", "old\nnext\n", "add " + row(2, "next") + ` "inserted\n"`, "old\ninserted\nnext\n"},
		{"EOF unterminated", "old\n", `append "next"`, "old\nnext"},
		{"EOF", "old\n", `append "next\n"`, "old\nnext\n"},
		{"EOF intentional blank", "old\n", `append "next\n\n"`, "old\nnext\n\n"},
		{"retain existing blank", "old\n\n", `type "old" "new"`, "new\n\n"},
		{"remove blank", "old\n\n", "type " + row(2, "") + ` ""`, "old\n"},
		{"empty existing", "", `append "new\n"`, "new\n"},
		{"delete all", "old\n", "type " + row(1, "old") + ` ""`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			edits := []FileEdit{{Path: "file.txt", Script: test.edit}}
			initial := map[string]string{"file.txt": test.before}
			writeTestFile(t, root, "file.txt", test.before, 0600)
			translated, err := translateForHostAtTest(t, root, edits, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := applyForHostAtTest(t, root, edits, ""); err != nil {
				t.Fatal(err)
			}
			if got := readTestFile(t, root, "file.txt"); got != test.want {
				t.Fatalf("engine bytes = %q, want %q", got, test.want)
			}
			hostWant := test.want
			if hostWant != "" && !strings.HasSuffix(hostWant, "\n") {
				hostWant += "\n"
			}
			t.Run("portable host", func(t *testing.T) {
				tree, err := patchtest.Apply(initial, string(translated.Patch))
				if err != nil || tree["file.txt"] != hostWant {
					t.Fatalf("host bytes = %q, want %q, error %v\n%s", tree["file.txt"], hostWant, err, translated.Patch)
				}
			})
			t.Run("native host", func(t *testing.T) {
				executable := os.Getenv("MEKUGI_TEST_APPLY_PATCH")
				if executable == "" {
					t.Skip("set MEKUGI_TEST_APPLY_PATCH for native executor validation")
				}
				directory := t.TempDir()
				writeTestFile(t, directory, "file.txt", test.before, 0600)
				command := exec.CommandContext(t.Context(), executable, string(translated.Patch))
				// Match the current host mode; legacy mode collapses EOF blanks.
				command.Env = append(os.Environ(), "CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS=1")
				command.Dir = directory
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("native apply_patch: %v\n%s", err, output)
				}
				data, err := os.ReadFile(filepath.Join(directory, "file.txt"))
				if err != nil || string(data) != hostWant {
					t.Fatalf("native bytes = %s, want %q, error %v\n%s", strconv.Quote(string(data)), hostWant, err, translated.Patch)
				}
			})
		})
	}
}
