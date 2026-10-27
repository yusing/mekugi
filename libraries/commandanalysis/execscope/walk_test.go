package execscope

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
