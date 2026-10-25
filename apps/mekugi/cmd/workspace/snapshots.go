package main

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// snapshotPackages finds snapshot declarations before go test compiles packages.
// Scan all platforms conservatively; Go still selects runnable files and tests.
func snapshotPackages(members []member) ([]string, error) {
	packages := map[string]bool{}
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
			if strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_") || !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Contains(data, []byte("TestUISnapshot")) && !bytes.Contains(data, []byte("TestSnapshot")) {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, data, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				if function, ok := decl.(*ast.FuncDecl); ok && function.Recv == nil && (strings.HasPrefix(function.Name.Name, "TestUISnapshot") || strings.HasPrefix(function.Name.Name, "TestSnapshot")) {
					// Patterns let Go skip directories containing only files for
					// another platform, just as the full workspace run does.
					packages["./"+filepath.ToSlash(filepath.Dir(path))+"/..."] = true
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(packages) == 0 {
		return nil, errors.New("workspace has no snapshot test packages")
	}
	result := make([]string, 0, len(packages))
	for path := range packages {
		result = append(result, path)
	}
	slices.Sort(result)
	return result, nil
}
