package router

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Explicit go fix package operands scope the Go sources the agent asks to edit.
func execGoPackageScope(input execProviderInput) execProviderResult {
	var roots []string
	for i := 1; i < len(input.args); i++ {
		arg := input.args[i]
		if arg == "-args" {
			break
		}
		if strings.HasPrefix(arg, "-") {
			if strings.Contains(arg, "=") {
				continue
			}
			if slices.Contains([]string{"-run", "-skip", "-tags", "-count", "-timeout", "-parallel", "-cpu", "-shuffle", "-coverpkg", "-coverprofile", "-blockprofile", "-cpuprofile", "-memprofile", "-mutexprofile", "-trace", "-o", "-exec", "-bench", "-benchtime", "-p", "-vet", "-mod", "-modfile", "-overlay", "-gcflags", "-ldflags", "-asmflags"}, arg) {
				i++
				continue
			}
			if slices.Contains([]string{"-v", "-x", "-n", "-race", "-msan", "-asan", "-cover", "-json", "-c", "-failfast", "-short", "-trimpath", "-work", "-a"}, arg) {
				continue
			}
			// An unknown flag may consume the following word as its value.
			break
		}
		if arg != "." && !strings.HasPrefix(arg, "./") && !strings.HasPrefix(arg, "../") && !filepath.IsAbs(arg) {
			continue
		}
		root := execProviderPath(input.cwd, strings.TrimSuffix(arg, "/..."))
		if info, err := os.Lstat(root); err == nil && info.IsDir() && !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		return execProviderResult{open: true, reason: "no literal local package directory"}
	}
	var paths []string
	seen := make(map[string]bool)
	queue := slices.Clone(roots)
	entries := 0
	truncated := false
	for len(queue) > 0 {
		root := queue[0]
		queue = queue[1:]
		if seen[root] {
			continue
		}
		seen[root] = true
		if time.Now().After(input.deadline) {
			truncated = true
			break
		}
		directory, err := os.Open(root)
		if err != nil {
			truncated = true
			continue
		}
		children, err := directory.ReadDir(maxExecListingEntries - entries + 1)
		directory.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			truncated = true
			continue
		}
		slices.SortFunc(children, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, child := range children {
			entries++
			if entries > maxExecListingEntries || len(paths) >= maxExecCaptureFiles || time.Now().After(input.deadline) {
				truncated = true
				break
			}
			path := filepath.Join(root, child.Name())
			if slices.Contains([]string{".git", ".hg", ".svn", ".jj"}, child.Name()) {
				continue
			}
			if child.IsDir() {
				if !execBuiltinPruned(path, child.Name()) {
					queue = append(queue, path)
				}
			} else if filepath.Ext(path) == ".go" {
				paths = append(paths, path)
			}
		}
		if truncated {
			break
		}
	}
	entry := execProviderFiles(paths, false)
	entry.Origin = "go " + input.args[0]
	result := execProviderResult{scope: []execScopeEntry{entry}, open: true}
	if truncated {
		result.reason = "package baseline enumeration reached its bound or was unavailable"
	}
	return result
}
