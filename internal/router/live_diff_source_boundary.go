package router

import (
	"context"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"slices"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
	"github.com/yusing/mekugi/internal/sourcekind"
	"mvdan.cc/sh/v3/syntax"
)

var liveDiffTSXLanguage = sitter.NewLanguage(typescript.LanguageTSX())

// liveDiffSourceReady reports whether a still-arriving source tip ends at a
// displayable target-language unit.
func liveDiffSourceReady(ctx context.Context, path, source string) bool {
	return liveDiffSourceTerminated(ctx, path, source) && liveDiffSourceComplete(ctx, path, source)
}

func liveDiffSourceLanguage(format sourcekind.Format) (*sitter.Language, bool) {
	switch format.Language {
	case "javascript":
		return codeModeJavaScriptLanguage, true
	case "typescript":
		if format.JSX {
			return liveDiffTSXLanguage, true
		}
		return execTypeScriptLanguage, true
	case "python":
		return execPythonLanguage, true
	}
	return nil, false
}

// A semicolon can end a statement without ending its source line. Check the
// target lexer, not the transporting shell/JSON string, before releasing it.
func liveDiffSourceTerminated(ctx context.Context, path, source string) bool {
	if strings.HasSuffix(source, "\n") {
		return true
	}
	if !strings.HasSuffix(source, ";") {
		return false
	}
	end := uint(len(source))
	if ext := filepath.Ext(path); ext == ".sh" || ext == ".bash" {
		parsed, _ := syntax.NewParser(syntax.RecoverErrors(4)).Parse(strings.NewReader(source), path)
		found := false
		syntax.Walk(parsed, func(node syntax.Node) bool {
			if node == nil || found || ctx.Err() != nil {
				return false
			}
			if stmt, ok := node.(*syntax.Stmt); ok && stmt.Semicolon.IsValid() && stmt.Semicolon.Offset()+1 == end {
				found = true
			}
			return !found
		})
		return found
	}
	format, _ := sourcekind.Classify(path)
	if format.Language == "go" {
		var lex scanner.Scanner
		file := token.NewFileSet().AddFile(path, -1, len(source))
		lex.Init(file, []byte(source), nil, 0)
		for ctx.Err() == nil {
			pos, kind, literal := lex.Scan()
			if kind == token.EOF {
				break
			}
			if kind == token.SEMICOLON && literal == ";" && file.Offset(pos)+1 == len(source) {
				return true
			}
		}
		return false
	}
	language, ok := liveDiffSourceLanguage(format)
	if !ok {
		return false
	}
	tree, err := parseExecSource([]byte(source), language, func() bool { return ctx.Err() != nil })
	if err != nil || tree == nil {
		return false
	}
	defer tree.Close()
	var terminal func(*sitter.Node, int) bool
	terminal = func(node *sitter.Node, depth int) bool {
		if node == nil || depth > 128 || ctx.Err() != nil || node.EndByte() != end {
			return false
		}
		if node.Kind() == ";" && !node.IsMissing() {
			return true
		}
		for i := node.ChildCount(); i > 0; i-- {
			if terminal(node.Child(uint(i-1)), depth+1) {
				return true
			}
		}
		return false
	}
	return terminal(tree.RootNode(), 0)
}

// Buffer unfinished code units in projected source, not just in the script
// transporting it. Unknown/text formats retain the decoded line boundary.
// Final snapshots bypass this display-only gate, including malformed input.
func liveDiffSourceComplete(ctx context.Context, path, source string) bool {
	if ext := filepath.Ext(path); ext == ".sh" || ext == ".bash" {
		_, err := syntax.NewParser().Parse(strings.NewReader(source), path)
		if err == nil {
			return true
		}
		parsed, _ := syntax.NewParser(syntax.RecoverErrors(4)).Parse(strings.NewReader(source), path)
		end := uint(len(strings.TrimRight(source, " \t\r\n")))
		complete := false
		syntax.Walk(parsed, func(node syntax.Node) bool {
			if node == nil || complete || ctx.Err() != nil {
				return false
			}
			switch node.(type) {
			case *syntax.Word, *syntax.Subshell:
				// A command inside an unfinished substitution/string is part
				// of that expression, not a standalone source statement.
				return false
			}
			if stmt, ok := node.(*syntax.Stmt); ok && stmt.End().Offset() == end {
				// Parser recovery can leave an incomplete word on a call. Only
				// accept an inner statement that parses independently in full.
				_, err := syntax.NewParser().Parse(strings.NewReader(source[stmt.Pos().Offset():end]), path)
				complete = err == nil
			}
			return !complete
		})
		return complete
	}
	format, _ := sourcekind.Classify(path)
	if format.Language == "go" {
		return liveDiffGoStatementEnd(ctx, source)
	}
	language, ok := liveDiffSourceLanguage(format)
	if !ok {
		return true
	}
	canceled := func() bool { return ctx.Err() != nil }
	tree, err := parseExecSource([]byte(source), language, canceled)
	if err != nil || tree == nil {
		return false
	}
	defer tree.Close()
	if !tree.RootNode().HasError() {
		return true
	}
	// Enclosing blocks and containers may still be open while their inner
	// units are complete. Close them with the parser's own bracket tokens; any
	// other unfinished syntax, such as a template string whose tail parses as
	// code, still fails the clean reparse.
	var closers []string
	var collect func(*sitter.Node, int) bool
	collect = func(node *sitter.Node, depth int) bool {
		if depth > 128 || ctx.Err() != nil {
			return false
		}
		if node.ChildCount() == 0 && !node.IsMissing() {
			switch kind := node.Kind(); kind {
			case "{", "${":
				closers = append(closers, "}")
			case "[":
				closers = append(closers, "]")
			case "(":
				closers = append(closers, ")")
			case "}", "]", ")":
				if len(closers) == 0 || closers[len(closers)-1] != kind {
					return false
				}
				closers = closers[:len(closers)-1]
			}
		}
		for i := range node.ChildCount() {
			if !collect(node.Child(uint(i)), depth+1) {
				return false
			}
		}
		return true
	}
	if !collect(tree.RootNode(), 0) || len(closers) == 0 {
		return false
	}
	slices.Reverse(closers)
	closed, err := parseExecSource([]byte(source+strings.Join(closers, "")), language, canceled)
	if err != nil || closed == nil {
		return false
	}
	defer closed.Close()
	if closed.RootNode().HasError() {
		return false
	}
	end := uint(len(strings.TrimRight(source, " \t\r\n")))
	var complete func(*sitter.Node, int) bool
	complete = func(node *sitter.Node, depth int) bool {
		if depth > 128 || ctx.Err() != nil {
			return false
		}
		kind := node.Kind()
		if node.EndByte() == end && (strings.HasSuffix(kind, "_statement") || strings.HasSuffix(kind, "_declaration")) {
			return true
		}
		if parent := node.Parent(); parent != nil && node.IsNamed() {
			// Class members are units of an open class body, like statements
			// in an open block. Object and array entries are not: an open
			// literal is still one unfinished statement.
			if liveDiffMemberBodies[parent.Kind()] {
				if node.EndByte() == end {
					return true
				}
				if next := node.NextSibling(); next != nil && next.EndByte() == end && (next.Kind() == "," || next.Kind() == ";") {
					return true
				}
			}
		}
		for i := range node.NamedChildCount() {
			if complete(node.NamedChild(uint(i)), depth+1) {
				return true
			}
		}
		return false
	}
	return complete(closed.RootNode(), 0)
}

var liveDiffMemberBodies = map[string]bool{"class_body": true, "interface_body": true, "object_type": true, "enum_body": true}

func liveDiffGoStatementEnd(ctx context.Context, source string) bool {
	var scan scanner.Scanner
	file := token.NewFileSet().AddFile("", -1, len(source))
	failed := false
	scan.Init(file, []byte(source), func(token.Position, string) { failed = true }, 0)
	last, first, end, depth := token.SEMICOLON, token.ILLEGAL, 0, 0
	for {
		pos, kind, literal := scan.Scan()
		if failed || ctx.Err() != nil {
			return false
		}
		if kind == token.EOF {
			break
		}
		if first == token.ILLEGAL {
			first = kind
		}
		last = kind
		if kind != token.SEMICOLON {
			if literal == "" {
				literal = kind.String()
			}
			end = file.Offset(pos) + len(literal)
		}
		switch kind {
		case token.LPAREN, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACK:
			depth--
		}
	}
	if depth != 0 || last != token.SEMICOLON {
		return false
	}
	if end == 0 {
		return true
	}
	prefixes := []string{"package preview\n", "package preview\nfunc _() {\n"}
	if first == token.PACKAGE {
		prefixes = []string{""}
	}
	for _, prefix := range prefixes {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, "", prefix+source, parser.SkipObjectResolution)
		if parsed == nil || ctx.Err() != nil {
			continue
		}
		if err == nil {
			return true
		}
		complete := false
		ast.Inspect(parsed, func(node ast.Node) bool {
			if node == nil || complete {
				return false
			}
			// Header init statements are not standalone reveal boundaries.
			// Recovery can manufacture a missing body after `for init;` or
			// `if init;`, leaving an otherwise complete AssignStmt underneath.
			var body *ast.BlockStmt
			control := true
			switch n := node.(type) {
			case *ast.ForStmt:
				body = n.Body
			case *ast.IfStmt:
				body = n.Body
			case *ast.SwitchStmt:
				body = n.Body
			case *ast.TypeSwitchStmt:
				body = n.Body
			default:
				control = false
			}
			if control && (body == nil || !body.Lbrace.IsValid() || fset.Position(body.Lbrace).Offset >= len(prefix)+end) {
				return false
			}
			switch node.(type) {
			case *ast.BadStmt, *ast.BadDecl:
				return false
			case ast.Stmt, ast.Decl:
				complete = fset.Position(node.End()).Offset == len(prefix)+end
			}
			return !complete
		})
		if complete {
			return true
		}
	}
	return false
}
