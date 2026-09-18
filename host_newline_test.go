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
		create                   bool
	}{
		{"new empty", "", `type ""`, "", true},
		{"new unterminated", "", `type "one"`, "one", true},
		{"new single LF", "", `type "one\n"`, "one\n", true},
		{"new intentional blank", "", `type "one\n\n"`, "one\n\n", true},
		{"new blank only", "", `type "\n"`, "\n", true},
		{"new heredoc", "", "type <<END\none\nEND\n", "one\n", true},
		{"new text frame", "", "type <<END\none\nEND\n", "one\n", true},
		{"authored literal LF", "old\nnext\n", "type \"old\" <<END\nnew\nEND\n", "new\n\nnext\n", false},
		{"insert before blank", "old\n\nnext\n", "add " + row(2, "") + ` "inserted\n"`, "old\ninserted\n\nnext\n", false},
		{"literal", "old\nnext\n", `type "old" "new"`, "new\nnext\n", false},
		{"row", "old\nnext\n", "type " + row(1, "old") + ` "new\n"`, "new\nnext\n", false},
		{"range", "old\nnext\n", "type " + row(1, "old") + ".." + row(2, "next") + ` "new\n"`, "new\n", false},
		{"before row", "old\nnext\n", "add " + row(2, "next") + ` "inserted\n"`, "old\ninserted\nnext\n", false},
		{"EOF unterminated", "old\n", `add EOF "next"`, "old\nnext", false},
		{"EOF", "old\n", `add EOF "next\n"`, "old\nnext\n", false},
		{"EOF intentional blank", "old\n", `add EOF "next\n\n"`, "old\nnext\n\n", false},
		{"retain existing blank", "old\n\n", `type "old" "new"`, "new\n\n", false},
		{"remove blank", "old\n\n", "type " + row(2, "") + ` ""`, "old\n", false},
		{"empty existing", "", `add EOF "new\n"`, "new\n", false},
		{"delete all", "old\n", "type " + row(1, "old") + ` ""`, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			script := "in file.txt\n" + test.edit
			initial := map[string]string{"file.txt": test.before}
			if test.create {
				script = "new file.txt\n" + test.edit
				initial = map[string]string{}
			} else {
				writeTestFile(t, root, "file.txt", test.before, 0600)
			}
			translated, err := translateForHostAtTest(t, root, script, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := applyForHostAtTest(t, root, script, ""); err != nil {
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
				if !test.create {
					writeTestFile(t, directory, "file.txt", test.before, 0600)
				}
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
