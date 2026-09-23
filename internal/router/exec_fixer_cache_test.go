package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func execFixerTestCapture(t *testing.T, root, workdir, command string) *execObservation {
	t.Helper()
	observation, ok := captureExecObservation(
		[]execCommandInput{{Command: command, Workdir: workdir, Shell: "bash"}},
		false, false, execCaptureEnv{directory: root, clock: t.TempDir()},
	)
	if !ok {
		t.Fatalf("%q was not observed", command)
	}
	return observation
}

func execFixerTestCapturedFiles(observation *execObservation) map[string]execFileSnapshot {
	files := make(map[string]execFileSnapshot, len(observation.Files))
	for _, file := range observation.Files {
		files[file.Path] = file
	}
	return files
}

func execFixerTestAssertExact(t *testing.T, root string, observation *execObservation, wantPaths []string) []mekugi.ReviewFile {
	t.Helper()
	reviews, complete, coverage, unswept := reconcileExecObservation(*observation, execReconcileEnv{})
	if !complete || coverage != execCoverageExact || unswept != "" {
		t.Fatalf("reconciliation complete=%v coverage=%q unswept=%q reviews=%+v", complete, coverage, unswept, reviews)
	}
	gotPaths := make([]string, 0, len(reviews))
	for _, review := range reviews {
		path := cmpOrPath(review.AfterPath, review.BeforePath)
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("review path %q outside root %q: %v", path, root, err)
		}
		gotPaths = append(gotPaths, filepath.ToSlash(relative))
	}
	slices.Sort(gotPaths)
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("review paths = %q, want %q; reviews=%+v", gotPaths, wantPaths, reviews)
	}
	return reviews
}

func execFixerTestRead(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestExecGofmtScopesGoFilesWithManagedOrigin(t *testing.T) {
	gofmt, err := exec.LookPath("gofmt")
	if err != nil {
		t.Skip("gofmt runtime is unavailable")
	}
	root := t.TempDir()
	bad := filepath.Join(root, "src", "bad.go")
	good := filepath.Join(root, "src", "good.go")
	nonGo := filepath.Join(root, "src", "keep.txt")
	writeTestFile(t, bad, "package p\nfunc f( ){return 1}\n")
	writeTestFile(t, good, "package p\n\nfunc g() {\n\treturn\n}\n")
	writeTestFile(t, nonGo, "stable text\n")
	before := execFixerTestRead(t, bad)
	observation := execFixerTestCapture(t, root, root, "gofmt -w .")
	if observation.Class != execScoped.String() {
		t.Fatalf("gofmt class = %q (%s), want scoped", observation.Class, observation.Reason)
	}
	captured := execFixerTestCapturedFiles(observation)
	if captured[bad].Content != before || captured[good].Content == "" {
		t.Fatalf("gofmt pre-call capture missing Go sources: bad=%+v good=%+v", captured[bad], captured[good])
	}
	if _, found := captured[nonGo]; found {
		t.Fatalf("gofmt captured non-Go file: %+v", captured[nonGo])
	}

	process := exec.Command(gofmt, "-w", ".")
	process.Dir = root
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("run gofmt: %v\n%s", err, output)
	}
	if execFixerTestRead(t, bad) == before {
		t.Fatal("gofmt fixture was already formatted")
	}
	reviews := execFixerTestAssertExact(t, root, observation, []string{"src/bad.go"})
	if reviews[0].Origin != "gofmt" || !strings.Contains(reviews[0].Diff, "package p") {
		t.Fatalf("gofmt review = %+v; want a tool-managed exact diff", reviews[0])
	}
	if execFixerTestRead(t, nonGo) != "stable text\n" {
		t.Fatal("gofmt changed the non-Go fixture")
	}
}

func TestExecGoModuleCommandsCaptureNearestManifestsAsManaged(t *testing.T) {
	for _, test := range []struct {
		name, command, origin, afterModule, afterSum string
	}{
		{
			name: "mod tidy", command: "go mod tidy", origin: "go mod tidy",
			afterModule: "module example.test/root\n\ngo 1.27\n// tidied\n", afterSum: "example.test/dep v1.2.3 h1:after\n",
		},
		{
			name: "get", command: "go get example.test/dep@v1.2.3", origin: "go get",
			afterModule: "module example.test/root\n\ngo 1.27\nrequire example.test/dep v1.2.3\n", afterSum: "example.test/dep v1.2.3 h1:after\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			nested := filepath.Join(root, "pkg", "subpkg")
			if err := os.MkdirAll(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			modulePath := filepath.Join(root, "go.mod")
			sumPath := filepath.Join(root, "go.sum")
			beforeModule := "module example.test/root\n\ngo 1.27\n"
			beforeSum := "example.test/dep v1.2.3 h1:before\n"
			writeTestFile(t, modulePath, beforeModule)
			writeTestFile(t, sumPath, beforeSum)
			observation := execFixerTestCapture(t, root, nested, test.command)
			if observation.Class != execScoped.String() {
				t.Fatalf("%s class = %q (%s), want scoped", test.command, observation.Class, observation.Reason)
			}
			captured := execFixerTestCapturedFiles(observation)
			if captured[modulePath].Content != beforeModule || captured[sumPath].Content != beforeSum {
				t.Fatalf("%s missed nearest module manifests: go.mod=%+v go.sum=%+v", test.command, captured[modulePath], captured[sumPath])
			}
			writeTestFile(t, modulePath, test.afterModule)
			writeTestFile(t, sumPath, test.afterSum)
			reviews := execFixerTestAssertExact(t, root, observation, []string{"go.mod", "go.sum"})
			for _, review := range reviews {
				if review.Origin != test.origin || review.Diff == "" {
					t.Errorf("%s review origin/diff = %q/%q, want managed origin %q with exact diff", test.command, review.Origin, review.Diff, test.origin)
				}
			}
		})
	}
}

func TestExecMixedPythonAndGoModKeepsDirectPathOrigin(t *testing.T) {
	root := t.TempDir()
	modulePath := filepath.Join(root, "go.mod")
	sumPath := filepath.Join(root, "go.sum")
	beforeModule := "module example.test/root\n\ngo 1.27\n"
	beforeSum := "example.test/dep v1.2.3 h1:before\n"
	writeTestFile(t, modulePath, beforeModule)
	writeTestFile(t, sumPath, beforeSum)
	command := `python3 -c 'from pathlib import Path; Path("go.mod").write_text("module example.test/root\\n\\ngo 1.27\\n// direct edit\\n")' && go mod tidy`
	observation := execFixerTestCapture(t, root, root, command)
	if observation.Class != execScoped.String() {
		t.Fatalf("mixed command class = %q (%s), want scoped", observation.Class, observation.Reason)
	}
	captured := execFixerTestCapturedFiles(observation)
	if captured[modulePath].Content != beforeModule || captured[sumPath].Content != beforeSum {
		t.Fatalf("mixed capture missed manifests: go.mod=%+v go.sum=%+v", captured[modulePath], captured[sumPath])
	}
	writeTestFile(t, modulePath, "module example.test/root\n\ngo 1.27\n// direct edit\n")
	writeTestFile(t, sumPath, "example.test/dep v1.2.3 h1:after\n")
	reviews := execFixerTestAssertExact(t, root, observation, []string{"go.mod", "go.sum"})
	byPath := make(map[string]mekugi.ReviewFile, len(reviews))
	for _, review := range reviews {
		byPath[filepath.Base(cmpOrPath(review.AfterPath, review.BeforePath))] = review
	}
	if review := byPath["go.mod"]; review.Origin != "" || review.OriginNote != "also changed by go mod tidy" ||
		!strings.Contains(review.Diff, "+// direct edit") {
		t.Fatalf("mixed go.mod review = %+v; want direct origin with go mod tidy note", review)
	}
	if review := byPath["go.sum"]; review.Origin != "go mod tidy" || !strings.Contains(review.Diff, "h1:after") {
		t.Fatalf("mixed go.sum review = %+v; want tool-managed origin", review)
	}
}

func TestExecLastSeenIsolatedAndDeletionEvicts(t *testing.T) {
	cache := &execLastSeen{}
	path := filepath.Join(t.TempDir(), "same.go")
	cache.put("workspace-a", "change-a", execFileSnapshot{Path: path, Kind: execFileText, Content: "from a\n"})
	cache.put("workspace-b", "change-b", execFileSnapshot{Path: path, Kind: execFileText, Content: "from b\n"})

	if prior, ok := cache.get("workspace-a", path); !ok || prior.change != "change-a" || prior.file.Content != "from a\n" {
		t.Fatalf("workspace-a cache entry = %+v, %v", prior, ok)
	}
	if prior, ok := cache.get("workspace-b", path); !ok || prior.change != "change-b" || prior.file.Content != "from b\n" {
		t.Fatalf("workspace-b cache entry = %+v, %v", prior, ok)
	}
	cache.put("workspace-a", "deleted", execFileSnapshot{Path: path, Kind: execFileAbsent})
	if prior, ok := cache.get("workspace-a", path); ok {
		t.Fatalf("deletion left stale cache entry: %+v", prior)
	}
	if prior, ok := cache.get("workspace-b", path); !ok || prior.change != "change-b" {
		t.Fatalf("deleting one namespace affected the other: %+v, %v", prior, ok)
	}
}

func TestExecLastSeenEnforces64MiBLRUByteBound(t *testing.T) {
	cache := &execLastSeen{}
	const contentBytes = 25 << 20
	content := strings.Repeat("x", contentBytes)
	put := func(name string) {
		cache.put("workspace", name, execFileSnapshot{
			Path: filepath.Join("/cache", name), Kind: execFileText, Content: content,
		})
	}
	put("a")
	put("b")
	put("c")
	if cache.bytes > maxExecLastSeenBytes {
		t.Fatalf("cache has %d bytes, exceeds %d", cache.bytes, maxExecLastSeenBytes)
	}
	if _, ok := cache.get("workspace", filepath.Join("/cache", "a")); ok {
		t.Fatal("oldest entry was not evicted after exceeding byte bound")
	}
	if _, ok := cache.get("workspace", filepath.Join("/cache", "b")); !ok {
		t.Fatal("recent entries missing after eviction")
	}
	put("d")
	if cache.bytes > maxExecLastSeenBytes {
		t.Fatalf("cache has %d bytes after LRU update, exceeds %d", cache.bytes, maxExecLastSeenBytes)
	}
	for name, want := range map[string]bool{"a": false, "b": true, "c": false, "d": true} {
		if _, ok := cache.get("workspace", filepath.Join("/cache", name)); ok != want {
			t.Errorf("entry %s present = %v, want %v", name, ok, want)
		}
	}
}

func TestExecLastSeenSweepKeepsPartialCoverageAndUnknownCounts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "edited.txt")
	writeTestFile(t, path, "before\n")
	cache := &execLastSeen{}
	cache.put("workspace-key", "prior-change", execFileSnapshot{Path: path, Kind: execFileText, Content: "before\n"})
	observation := execFixerTestCapture(t, root, root, "make build")
	writeTestFile(t, path, "after\n")
	reviews, complete, coverage, unswept := reconcileExecObservation(*observation, execReconcileEnv{
		seen: cache, cacheNamespace: "workspace-key",
	})
	if !complete || coverage != execCoveragePartial || unswept != "" || len(reviews) != 1 {
		t.Fatalf("last-seen reconciliation complete=%v coverage=%q unswept=%q reviews=%+v", complete, coverage, unswept, reviews)
	}
	review := reviews[0]
	if review.Incomplete != "since last observed (change prior-change)" ||
		!strings.Contains(review.Diff, "-before") || !strings.Contains(review.Diff, "+after") {
		t.Fatalf("last-seen diff = %+v; want explicit prior-observation label and diff", review)
	}
	added, removed := review.LineCounts()
	if added != -1 || removed != -1 {
		t.Fatalf("last-seen line counts = %d/%d, want unknown (-1/-1)", added, removed)
	}
}
