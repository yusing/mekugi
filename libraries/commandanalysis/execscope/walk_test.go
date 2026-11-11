package execscope

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBoundedWalkPreservesCompleteTraversalAndBudgets(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"a/z", "a/a", "b", "c/x"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var want []string
	if err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error { want = append(want, path); return err }); err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{len(want) - 1, len(want), len(want) + 1} {
		w := execPathWalker{remaining: budget, deadline: time.Now().Add(time.Minute)}
		var got []string
		err := w.walk(root, func(path string, _ fs.DirEntry, err error) error { got = append(got, path); return err })
		if budget < len(want) {
			if !errors.Is(err, errExecWalkIncomplete) || len(got) > budget {
				t.Fatalf("incomplete traversal: %v, %v", got, err)
			}
		} else if err != nil || !slices.Equal(got, want) {
			t.Fatalf("complete traversal differs: %v, %v", got, err)
		}
	}
	w := execPathWalker{remaining: 100, deadline: time.Now().Add(-time.Second)}
	if err := w.walk(root, func(string, fs.DirEntry, error) error { t.Fatal("expired walk visited source"); return nil }); !errors.Is(err, errExecWalkIncomplete) {
		t.Fatal(err)
	}
}

func BenchmarkBoundedWideWalk(b *testing.B) {
	for _, count := range []int{5000, 20000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			root := b.TempDir()
			for i := range count {
				if err := os.WriteFile(filepath.Join(root, fmt.Sprint(i)), nil, 0600); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				w := execPathWalker{remaining: 4096, deadline: time.Now().Add(time.Minute)}
				if err := w.walk(root, func(string, fs.DirEntry, error) error { return nil }); !errors.Is(err, errExecWalkIncomplete) {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestBoundedGlobMatchesCompleteEnumeration(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a/x.go", "a/y.txt", "b/z.go", "literal[.go"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"*/*.go", "link/*.go", "[ab]/*", "missing/*", "literal[[]*.go", "[", "*/["} {
		pattern = filepath.Join(root, pattern)
		want, wantErr := filepath.Glob(pattern)
		w := execPathWalker{remaining: 100, deadline: time.Now().Add(time.Minute)}
		got, err := w.glob(pattern)
		if !slices.Equal(got, want) || !errors.Is(err, wantErr) {
			t.Fatalf("glob %q: %v %v, want %v %v", pattern, got, err, want, wantErr)
		}
	}
	// Both operands consume the same directory-entry budget.
	w := execPathWalker{remaining: 4, deadline: time.Now().Add(time.Minute)}
	if _, err := w.glob(filepath.Join(root, "*")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.glob(filepath.Join(root, "a", "*")); !errors.Is(err, errExecWalkIncomplete) {
		t.Fatalf("second operand escaped budget: %v", err)
	}
}

func TestAuxiliaryGlobsPreserveOperandsAndBackups(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a/x.txt", "a/z.txt", "b/y.txt"} {
		writeTestFile(t, filepath.Join(root, name), "before\n")
	}
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		command string
		class   Class
		want    []string
	}{
		{"hg revert 'link/*.txt' 'b/*.txt'", Scoped, []string{"link/x.txt", "link/x.txt.orig", "link/z.txt", "link/z.txt.orig", "b/y.txt", "b/y.txt.orig"}},
		{"hg revert -C 'link/*.txt'", Scoped, []string{"link/x.txt", "link/z.txt"}},
		{"hg revert --no-backup 'link/*.txt'", Scoped, []string{"link/x.txt", "link/z.txt"}},
		{"sed -i.bak 's/before/after/' link/*.txt b/*.txt", Declared, []string{"link/x.txt.bak", "link/z.txt.bak", "b/y.txt.bak"}},
		{"perl -pi.bak -e 's/before/after/' link/*.txt b/*.txt", Declared, []string{"link/x.txt.bak", "link/z.txt.bak", "b/y.txt.bak"}},
		{"sed -i'backup/*' 's/before/after/' link/*.txt", Declared, []string{"backup/link/x.txt", "backup/link/z.txt"}},
		{"sed -i'backup/*' 's/before/after/' " + root + "/link/*.txt", Declared, []string{"backup" + root + "/link/x.txt", "backup" + root + "/link/z.txt"}},
		{"sed -i.bak 's/before/after/' missing/*.txt", Declared, nil},
	} {
		t.Run(test.command, func(t *testing.T) {
			plan := Classify(Input{Command: test.command, Directory: root, Shell: "bash"})
			var got []string
			for _, entry := range plan.Scope {
				for _, operand := range entry.Operands {
					if !operand.Glob {
						got = append(got, strings.TrimPrefix(operand.Path, root+"/"))
					}
				}
			}
			if plan.Class != test.class || !slices.Equal(got, test.want) {
				t.Fatalf("class=%s paths=%q reason=%q; want %s %q", plan.Class, got, plan.Reason, test.class, test.want)
			}
		})
	}
}

func TestAuxiliaryGlobsShareOperandBudget(t *testing.T) {
	root := t.TempDir()
	// Each operand fits separately, but their union exceeds one allowance.
	for _, directory := range []string{"a", "b"} {
		for i := range ListingLimit/2 + 1 {
			writeTestFile(t, filepath.Join(root, directory, fmt.Sprintf("%04d.txt", i)), "")
		}
	}
	for _, command := range []string{
		"hg revert 'a/*.txt' 'b/*.txt'",
		"sed -i.bak 's/a/b/' a/*.txt b/*.txt",
		"perl -pi.bak -e 's/a/b/' a/*.txt b/*.txt",
	} {
		t.Run(command, func(t *testing.T) {
			plan := Classify(Input{Command: command, Directory: root, Shell: "bash", Deadline: time.Now().Add(time.Minute)})
			var acquired int
			for _, entry := range plan.Scope {
				for _, operand := range entry.Operands {
					if strings.HasSuffix(operand.Path, ".orig") || strings.HasSuffix(operand.Path, ".bak") {
						acquired++
					}
				}
			}
			if plan.Class < Scoped || !strings.Contains(plan.Reason, "enumeration budget") || acquired != ListingLimit {
				t.Fatalf("class=%s acquired=%d reason=%q", plan.Class, acquired, plan.Reason)
			}
		})
	}
}
