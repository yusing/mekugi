package main

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// affectedSnapshotPackages narrows only inputs with known snapshot ownership.
// Other resources, build configuration and tooling retain the full gate. Import
// discovery includes tests and every platform, so a shared renderer change also
// checks downstream snapshots, not just the renderer's own fixtures.
func affectedSnapshotPackages(members []member, packages []string, changed []byte) ([]string, error) {
	if len(changed) == 0 {
		return packages, nil
	}
	if changed[len(changed)-1] != 0 {
		return nil, fmt.Errorf("snapshot changed paths must be NUL-terminated")
	}
	sources, fixtures := map[string]bool{}, map[string]bool{}
	for _, raw := range bytes.Split(changed[:len(changed)-1], []byte{0}) {
		path := string(raw)
		// The runner is an executable consumer, not a Go import dependency.
		if strings.HasPrefix(path, "apps/mekugi/cmd/workspace/") {
			return packages, nil
		}
		owned := false
		for _, m := range members {
			if strings.HasPrefix(path, filepath.ToSlash(m.directory)+"/") {
				owned = true
				break
			}
		}
		if !owned {
			return packages, nil
		}
		for _, part := range strings.Split(path, "/") {
			if strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") || part == "vendor" {
				return packages, nil
			}
		}
		if owner, _, ok := strings.Cut(path, "/testdata/snapshots/"); ok && strings.HasSuffix(path, ".txt") {
			// Baselines are read by their owner's harness, not its importers.
			known := false
			for _, pattern := range packages {
				if pattern == "./"+owner+"/..." {
					known = true
					break
				}
			}
			if !known {
				return packages, nil
			}
			fixtures[owner] = true
		} else if strings.HasSuffix(path, ".go") && !strings.Contains(path, "/testdata/") {
			sources[filepath.ToSlash(filepath.Dir(path))] = true
		} else {
			return packages, nil
		}
	}
	if len(sources) != 0 {
		importers, err := snapshotImporters(members)
		if err != nil {
			return nil, err
		}
		queue := make([]string, 0, len(sources))
		for source := range sources {
			queue = append(queue, source)
		}
		for i := 0; i < len(queue); i++ {
			for _, importer := range importers[queue[i]] {
				if !sources[importer] {
					sources[importer] = true
					queue = append(queue, importer)
				}
			}
		}
	}
	var selected []string
nextPackage:
	for _, pattern := range packages {
		root := strings.TrimSuffix(strings.TrimPrefix(pattern, "./"), "/...")
		for _, affected := range []map[string]bool{sources, fixtures} {
			for path := range affected {
				if path == root || strings.HasPrefix(path, root+"/") {
					selected = append(selected, pattern)
					continue nextPackage
				}
			}
		}
	}
	return selected, nil
}

func snapshotImporters(members []member) (map[string][]string, error) {
	importers := map[string][]string{}
	for _, m := range members {
		err := filepath.WalkDir(m.directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if path != m.directory {
					if strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_") || entry.Name() == "testdata" || entry.Name() == "vendor" {
						return filepath.SkipDir
					}
					if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				name, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if dependency, local := strings.CutPrefix(name, prefix+"/"); local {
					importers[dependency] = append(importers[dependency], filepath.ToSlash(filepath.Dir(path)))
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return importers, nil
}
