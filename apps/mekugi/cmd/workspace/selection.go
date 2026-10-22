package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

// applicationSelection reads the direct Composition.Add declarations used by
// frontendDeclarations. Unsupported composition uses fail instead of yielding a
// partial list. Execution and renderer-only consumers share this declaration owner.
func applicationSelection(path string, members []member) (map[string]bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}
	memberPaths := make(map[string]string, len(members))
	for _, m := range members {
		if directory := filepath.ToSlash(m.directory); strings.HasPrefix(directory, "modules/") {
			memberPaths[m.path] = directory
		}
	}
	imports := make(map[string]string)
	for _, imported := range file.Imports {
		name, _ := strconv.Unquote(imported.Path.Value)
		if directory, ok := memberPaths[name]; ok {
			alias := filepath.Base(name)
			if imported.Name != nil {
				alias = imported.Name.Name
			}
			imports[alias] = directory
		}
	}
	var declarations *ast.FuncDecl
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "frontendDeclarations" {
			declarations = function
			break
		}
	}
	unsupported := func() error {
		return fmt.Errorf("%s: unsupported composition shape; expected direct c.Add(package.Module, dependencies) declarations and return &c in frontendDeclarations", path)
	}
	if declarations == nil || declarations.Body == nil {
		return nil, unsupported()
	}
	allowed := make(map[*ast.Ident]bool)
	shadowed := make(map[string]token.Pos)
	var composition string
	for _, statement := range declarations.Body.List {
		if assignment, ok := statement.(*ast.AssignStmt); ok && assignment.Tok == token.DEFINE {
			for _, left := range assignment.Lhs {
				if name, ok := left.(*ast.Ident); ok && imports[name.Name] != "" && shadowed[name.Name] == token.NoPos {
					shadowed[name.Name] = assignment.End()
				}
			}
		}
		declaration, ok := statement.(*ast.DeclStmt)
		if !ok {
			continue
		}
		general, ok := declaration.Decl.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range value.Names {
				if imports[name.Name] != "" && shadowed[name.Name] == token.NoPos {
					shadowed[name.Name] = value.End()
				}
			}
			typeName, ok := value.Type.(*ast.Ident)
			if !ok || typeName.Name != "Composition" {
				continue
			}
			if composition != "" || len(value.Names) != 1 || len(value.Values) != 0 {
				return nil, unsupported()
			}
			composition = value.Names[0].Name
			allowed[value.Names[0]] = true
		}
	}
	if composition == "" {
		return nil, unsupported()
	}
	selected := make(map[string]bool)
	returned := false
	for _, statement := range declarations.Body.List {
		var expression ast.Expr
		switch statement := statement.(type) {
		case *ast.ReturnStmt:
			if len(statement.Results) > 0 {
				if address, ok := statement.Results[0].(*ast.UnaryExpr); ok && address.Op == token.AND {
					if value, ok := address.X.(*ast.Ident); ok && value.Name == composition {
						allowed[value], returned = true, true
					}
				}
			}
		case *ast.AssignStmt:
			if len(statement.Rhs) == 1 {
				expression = statement.Rhs[0]
			}
		case *ast.ExprStmt:
			expression = statement.X
		}
		call, ok := expression.(*ast.CallExpr)
		if !ok {
			continue
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		receiver, ok := method.X.(*ast.Ident)
		if !ok || receiver.Name != composition {
			continue
		}
		if method.Sel.Name != "Add" || len(call.Args) != 2 {
			return nil, unsupported()
		}
		descriptor, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok || descriptor.Sel.Name != "Module" {
			return nil, unsupported()
		}
		owner, ok := descriptor.X.(*ast.Ident)
		if !ok || imports[owner.Name] == "" || shadowed[owner.Name] != token.NoPos && shadowed[owner.Name] < owner.Pos() {
			return nil, unsupported()
		}
		directory := imports[owner.Name]
		if selected[directory] {
			return nil, fmt.Errorf("%s: repeated module selection %s", path, directory)
		}
		selected[directory] = true
		allowed[receiver] = true
	}
	if !returned {
		return nil, unsupported()
	}
	ast.Inspect(declarations.Body, func(node ast.Node) bool {
		if identifier, ok := node.(*ast.Ident); ok && identifier.Name == composition && !allowed[identifier] {
			err = unsupported()
		}
		return err == nil
	})
	if err != nil {
		return nil, err
	}
	return selected, nil
}
