package toolplugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Opt in explicitly: normal package tests never need an installed language server.
// This acceptance test uses real semantic indexing, not the protocol fixture.
func TestNativeSymbolRealGoplsReferences(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_REAL_GOPLS") != "1" {
		t.Skip("set MEKUGI_TEST_REAL_GOPLS=1 to exercise installed gopls")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Fatalf("real-gopls acceptance requested but gopls is unavailable: %v", err)
	}
	t.Setenv("GOWORK", "off")
	root := t.TempDir()
	write := func(path, source string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/symbolfixture\n\ngo 1.23\n")
	write("core/core.go", "package core\n\nfunc Pick() int { return 1 }\n// Pick is also mentioned in non-code.\nconst marker = \"Pick\"\n")
	write("caller/caller.go", "package caller\n\nimport target \"example.com/symbolfixture/core\"\n\nfunc Use() int { return target.Pick() }\nvar Invoke = target.Pick\n")
	write("core/core_test.go", "package core\n\nimport \"testing\"\n\nfunc TestInternal(t *testing.T) { _ = Pick() }\n")
	write("core/external_test.go", "package core_test\n\nimport (\n target \"example.com/symbolfixture/core\"\n \"testing\"\n)\n\nfunc TestExternal(t *testing.T) { _ = target.Pick() }\n")
	write("unrelated/unrelated.go", "package unrelated\n\nfunc Pick() int { return 2 }\nfunc Use() int { return Pick() }\n")

	key := func(path, row string) string {
		return filepath.ToSlash(filepath.Join(root, path)) + ":" + row
	}
	want := map[string]int{
		key("core/core.go", "3 func Pick() int { return 1 }"):                                   1,
		key("caller/caller.go", "5 func Use() int { return target.Pick() }"):                    1,
		key("caller/caller.go", "6 var Invoke = target.Pick"):                                   1,
		key("core/core_test.go", "5 func TestInternal(t *testing.T) { _ = Pick() }"):            1,
		key("core/external_test.go", "8 func TestExternal(t *testing.T) { _ = target.Pick() }"): 1,
	}
	check := func(t *testing.T, expected map[string]int) {
		t.Helper()
		for _, tuples := range []int{1, 2} {
			t.Run(fmt.Sprintf("%d-tuples", tuples), func(t *testing.T) {
				args := []string{"--workspace", root, "--max-tokens", "4000"}
				for range tuples {
					args = append(args, "refs", "core/core.go", "Pick")
				}
				out := nativeExecute(t, "msymbol", args...)
				if out.ExitCode != 0 || out.Stderr != "" || out.OmittedOutput != nil {
					t.Fatalf("semantic query failed or incomplete: %+v", out)
				}
				got := nativeRealGoplsReferenceRows(t, out.Stdout)
				counts := make(map[string]int, len(expected))
				for row, n := range expected {
					counts[row] = n * tuples
				}
				if !reflect.DeepEqual(got, counts) {
					t.Fatalf("exact semantic rows differ (including declaration and test variants):\ngot: %#v\nwant: %#v\noutput:\n%s", got, counts, out.Stdout)
				}
			})
		}
	}
	t.Run("initial", func(t *testing.T) { check(t, want) })

	// Change both row positions and contents, remove a reference, and add a file.
	// A cached package graph or cached source rows must not survive the next query.
	write("caller/caller.go", "package caller\n\nimport target \"example.com/symbolfixture/core\"\n\n// Shift the caller and replace the old source row.\nfunc Use() int { return target.Pick() + 10 }\n")
	write("caller/added.go", "package caller\n\nimport target \"example.com/symbolfixture/core\"\n\nfunc Added() int { return target.Pick() }\n")
	delete(want, key("caller/caller.go", "5 func Use() int { return target.Pick() }"))
	delete(want, key("caller/caller.go", "6 var Invoke = target.Pick"))
	want[key("caller/caller.go", "6 func Use() int { return target.Pick() + 10 }")] = 1
	want[key("caller/added.go", "5 func Added() int { return target.Pick() }")] = 1
	t.Run("after-source-edit", func(t *testing.T) { check(t, want) })
}

// Ignore resolver ordering only. Preserve exact paths, line numbers, source rows,
// and multiplicity so duplicates, missing references, and stale text all fail.
func nativeRealGoplsReferenceRows(t *testing.T, output string) map[string]int {
	t.Helper()
	rows := map[string]int{}
	path := ""
	for row := range strings.SplitSeq(strings.TrimSuffix(output, "\n"), "\n") {
		if strings.HasPrefix(row, "\"") && strings.HasSuffix(row, "\":") {
			var err error
			path, err = strconv.Unquote(strings.TrimSuffix(row, ":"))
			if err != nil {
				t.Fatalf("invalid reference path header %q: %v", row, err)
			}
			continue
		}
		line, _, found := strings.Cut(row, " ")
		if n, err := strconv.Atoi(line); path == "" || !found || err != nil || n <= 0 {
			t.Fatalf("invalid reference row %q under %q", row, path)
		}
		rows[path+":"+row]++
	}
	return rows
}
