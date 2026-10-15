package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// checkRequires checks the declarative module.go surface against production
// imports and the module's typed Deps. Module IDs are repository leaf names.
func checkRequires(m member) error {
	imports, deps, requires := map[string]bool{}, map[string]bool{}, map[string]bool{}
	found := false
	err := filepath.WalkDir(m.directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != m.directory && (entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		aliases := map[string]string{}
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
			alias := filepath.Base(name)
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			imports[id], aliases[alias] = true, id
		}
		if filepath.Dir(path) != m.directory {
			return nil
		}
		for _, decl := range file.Decls {
			general, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range general.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if spec.Name.Name != "Deps" {
						continue
					}
					ast.Inspect(spec.Type, func(node ast.Node) bool {
						if selector, ok := node.(*ast.SelectorExpr); ok {
							if alias, ok := selector.X.(*ast.Ident); ok && aliases[alias.Name] != "" {
								deps[aliases[alias.Name]] = true
							}
						}
						return true
					})
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
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s: missing Module descriptor in module.go", m.directory)
	}
	if !maps.Equal(requires, imports) || !maps.Equal(requires, deps) {
		return fmt.Errorf("%s: Requires %v, imported module APIs %v, Deps module APIs %v must match", m.directory, slices.Sorted(maps.Keys(requires)), slices.Sorted(maps.Keys(imports)), slices.Sorted(maps.Keys(deps)))
	}
	return nil
}
