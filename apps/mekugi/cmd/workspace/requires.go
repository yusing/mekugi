package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// checkRequires checks the declarative module.go surface against production
// imports. Module IDs are repository leaf names.
func checkRequires(m member, files map[string]*ast.File) error {
	imports, requires := map[string]bool{}, map[string]bool{}
	found := false
	for _, path := range slices.Sorted(maps.Keys(files)) {
		file := files[path]
		for _, imp := range file.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			relative, ok := strings.CutPrefix(name, prefix+"/modules/")
			if !ok {
				continue
			}
			id, _, _ := strings.Cut(relative, "/")
			if id == filepath.Base(m.directory) {
				continue
			}
			imports[id] = true
		}
		if filepath.Dir(path) != m.directory {
			continue
		}
		for _, decl := range file.Decls {
			general, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range general.Specs {
				switch spec := spec.(type) {
				case *ast.ValueSpec:
					if filepath.Base(path) != "module.go" || len(spec.Names) != 1 || spec.Names[0].Name != "Module" || len(spec.Values) != 1 {
						continue
					}
					literal, ok := spec.Values[0].(*ast.CompositeLit)
					if !ok {
						return fmt.Errorf("%s: Module must be a descriptor literal", path)
					}
					found = true
					for _, field := range literal.Elts {
						pair, ok := field.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := pair.Key.(*ast.Ident)
						if !ok || key.Name != "Requires" {
							continue
						}
						list, ok := pair.Value.(*ast.CompositeLit)
						if !ok {
							return fmt.Errorf("%s: Requires must be a literal list", path)
						}
						for _, value := range list.Elts {
							text, ok := value.(*ast.BasicLit)
							if !ok || text.Kind != token.STRING {
								return fmt.Errorf("%s: Requires must contain literal module IDs", path)
							}
							id, _ := strconv.Unquote(text.Value)
							if requires[id] {
								return fmt.Errorf("%s: repeated requirement %s", path, id)
							}
							requires[id] = true
						}
					}
				}
			}
		}
	}
	if !found {
		return fmt.Errorf("%s: missing Module descriptor in module.go", m.directory)
	}
	if !maps.Equal(requires, imports) {
		return fmt.Errorf("%s: Requires %v and imported module APIs %v must match", m.directory, slices.Sorted(maps.Keys(requires)), slices.Sorted(maps.Keys(imports)))
	}
	return nil
}
