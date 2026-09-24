// Package gooutline supplies declaration boundaries from the host Go grammar.
package gooutline

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"strconv"
)

type Entry struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Receiver string `json:"receiver,omitempty"`
	From     int    `json:"from"`
	To       int    `json:"to"`
	NameFrom int    `json:"name_from"`
	NameTo   int    `json:"name_to"`
	Complete bool   `json:"complete"`
}

type Diagnostic struct {
	Offset int `json:"offset"`
}

type Document struct {
	Entries []Entry      `json:"entries"`
	Errors  []Diagnostic `json:"errors"`
}

// Parse returns byte offsets into source. Parsing never resolves imports or
// evaluates code; syntax errors invalidate only declarations they intersect.
func Parse(source string) Document {
	result := Document{Entries: []Entry{}, Errors: []Diagnostic{}}
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "source.go", source, parser.AllErrors|parser.SkipObjectResolution)
	if list, ok := err.(scanner.ErrorList); ok {
		for _, diagnostic := range list {
			result.Errors = append(result.Errors, Diagnostic{Offset: diagnostic.Pos.Offset})
		}
	}
	if file == nil {
		return result
	}
	offset := func(position token.Pos) int { return files.PositionFor(position, false).Offset }
	add := func(kind, name, receiver string, node ast.Node, identifier *ast.Ident) {
		entry := Entry{Kind: kind, Name: name, Receiver: receiver, From: offset(node.Pos()), To: offset(node.End()), NameFrom: -1, NameTo: -1, Complete: true}
		if identifier != nil {
			entry.NameFrom, entry.NameTo = offset(identifier.Pos()), offset(identifier.End())
		}
		if !node.End().IsValid() || entry.To < entry.From {
			entry.Complete = false
			entry.To = max(entry.From, entry.NameTo)
		}
		for _, diagnostic := range result.Errors {
			if diagnostic.Offset >= entry.From && diagnostic.Offset <= entry.To {
				entry.Complete = false
			}
		}
		ast.Inspect(node, func(child ast.Node) bool {
			switch child.(type) {
			case *ast.BadDecl, *ast.BadExpr, *ast.BadStmt:
				entry.Complete = false
			}
			return true
		})
		result.Entries = append(result.Entries, entry)
	}
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			kind, receiver := "function", ""
			if declaration.Recv != nil && len(declaration.Recv.List) > 0 {
				kind = "method"
				var text bytes.Buffer
				if format.Node(&text, files, declaration.Recv.List[0].Type) == nil {
					receiver = text.String()
				}
			}
			add(kind, declaration.Name.Name, receiver, declaration, declaration.Name)
		case *ast.GenDecl:
			for _, specification := range declaration.Specs {
				switch specification := specification.(type) {
				case *ast.ImportSpec:
					name, err := strconv.Unquote(specification.Path.Value)
					if err == nil {
						add("import", name, "", specification, nil)
					}
				case *ast.TypeSpec:
					add("type", specification.Name.Name, "", specification, specification.Name)
				case *ast.ValueSpec:
					kind := "variable"
					if declaration.Tok == token.CONST {
						kind = "constant"
					}
					for _, name := range specification.Names {
						add(kind, name.Name, "", specification, name)
					}
				}
			}
		}
	}
	return result
}
