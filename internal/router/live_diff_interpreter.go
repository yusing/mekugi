package router

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

// Literal predictions are display-only. Never evaluate an expression or use
// these bytes as the observed outcome of a host command.
func liveDiffInterpreterWrite(ctx context.Context, stmt *syntax.Stmt, directory string) ([]mekugi.ReviewFile, bool, error) {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Negated || len(call.Assigns) != 0 {
		return nil, false, nil
	}
	words, ok := literalArgs(call.Args)
	if !ok || len(words) == 0 {
		return nil, false, nil
	}
	identity := shellsyntax.InterpreterIdentity(words[0])
	python := strings.HasPrefix(identity, "python")
	if !python && identity != "node" && identity != "bun" && identity != "deno" {
		return nil, false, nil
	}
	input := execProviderInput{identity: identity, args: words[1:], cwd: directory, deadline: time.Now().Add(execProviderBudget)}
	for _, redirect := range stmt.Redirs {
		if redirect.Op != syntax.Hdoc && redirect.Op != syntax.DashHdoc {
			return nil, false, nil
		}
		body, literal := liveDiffShellHeredoc(redirect, false)
		if !literal {
			return nil, false, nil
		}
		input.stdin = body
	}
	source, script, reason := execProgramSource(input)
	if reason != "" || len(source) > maxExecProgramBytes {
		return nil, false, nil
	}
	language := codeModeJavaScriptLanguage
	if python {
		language = execPythonLanguage
	} else if strings.HasSuffix(script, ".ts") || identity == "deno" {
		language = execTypeScriptLanguage
	}
	data := []byte(source)
	tree, err := parseExecSource(data, language, func() bool { return ctx.Err() != nil || time.Now().After(input.deadline) })
	if err != nil {
		return nil, false, err
	}
	if tree == nil {
		return nil, false, nil
	}
	defer tree.Close()
	if tree.RootNode().HasError() {
		return nil, false, nil
	}
	scan := execSourceScope{input: input, source: data, script: script, python: python, vars: make(map[string][]string), texts: make(map[string]bool), assigned: make(map[string]int), aliases: make(map[string]string)}
	scan.walk(tree.RootNode())
	// Capture walks assignments in execution order, but preview resolves all
	// writes after scanning. A final reassigned value must not retarget an
	// earlier write in the provisional diff.
	for name, count := range scan.assigned {
		if count > 1 {
			delete(scan.vars, name)
		}
	}
	var files []mekugi.ReviewFile
	seen := make(map[string]bool)
	var failure error
	var visit func(*sitter.Node, int)
	visit = func(node *sitter.Node, depth int) {
		if node == nil || failure != nil {
			return
		}
		if depth > 128 || ctx.Err() != nil || time.Now().After(input.deadline) || len(files) > 32 {
			failure = errors.New("literal preview capacity exceeded")
			return
		}
		switch node.Kind() {
		case "function_definition", "function_declaration", "function_expression", "arrow_function", "method_definition", "if_statement", "for_statement", "for_in_statement", "while_statement", "try_statement", "conditional_expression", "ternary_expression", "binary_expression", "boolean_operator", "list_comprehension":
			failure = errors.New("literal preview includes unsupported control flow")
			return
		}
		function, args := sourceCall(node)
		if function != nil {
			if strings.HasSuffix(scan.text(function), ".chdir") {
				failure = errors.New("literal preview does not predict working-directory changes")
				return
			}
			path, content, recognized := scan.literalWrite(function, args)
			if recognized {
				if seen[path] || !filepath.IsAbs(path) || len(content) > liveDiffPreviewFileLimit || !utf8.ValidString(content) {
					failure = errors.New("literal preview has dependent or unsupported writes")
					return
				}
				before, exists, err := liveDiffSourceRead(ctx, path, liveDiffPreviewFile)
				if err != nil {
					failure = err
					return
				}
				beforePath := path
				if !exists {
					beforePath = ""
				}
				files = append(files, mekugi.RenderReviewFile(beforePath, path, before, content))
				seen[path] = true
				return
			}
			name := scan.text(function)
			base := name[strings.LastIndexByte(name, '.')+1:]
			if alias := scan.aliases[base]; alias != "" {
				base = alias
			}
			base = strings.TrimSuffix(base, "Sync")
			switch base {
			case "Path", "read_text", "join", "resolve":
				// Syntax-only path construction or a read used by this subset.
			case "require":
				module := ""
				if len(args) == 1 {
					module, _ = scan.literal(args[0])
				}
				module = strings.TrimPrefix(module, "node:")
				if module != "fs" && module != "fs/promises" && module != "path" {
					failure = errors.New("literal preview includes an unresolved module")
					return
				}
			default:
				failure = errors.New("literal preview includes an unsupported earlier or unresolved effect")
				return
			}
		}
		for i := range node.NamedChildCount() {
			visit(node.NamedChild(uint(i)), depth+1)
		}
	}
	visit(tree.RootNode(), 0)
	if failure != nil {
		return nil, false, nil
	}
	return files, len(files) != 0, failure
}

func (s *execSourceScope) literalWrite(function *sitter.Node, args []*sitter.Node) (string, string, bool) {
	name := s.text(function)
	base := name[strings.LastIndexByte(name, '.')+1:]
	if alias := s.aliases[base]; alias != "" {
		base = alias
	}
	var paths []string
	var body *sitter.Node
	if !s.python {
		if (base != "writeFileSync" && base != "writeFile") || len(args) != 2 {
			return "", "", false
		}
		paths, body = s.paths(args[0]), args[1]
	} else {
		object := function.ChildByFieldName("object")
		if base == "write_text" && len(args) == 1 {
			paths, body = s.paths(object), args[0]
		} else if base == "write" && len(args) == 1 {
			open, values := sourceCall(object)
			if s.text(open) != "open" || len(values) != 2 {
				return "", "", false
			}
			mode, _ := s.literal(values[1])
			if mode != "w" {
				return "", "", false
			}
			paths, body = s.paths(values[0]), args[0]
		} else {
			return "", "", false
		}
	}
	if len(paths) != 1 {
		return "", "", false
	}
	if content, ok := s.literal(body); ok {
		return paths[0], content, true
	}
	if !s.python {
		return "", "", false
	}
	replace, values := sourceCall(body)
	if replace == nil || s.text(replace.ChildByFieldName("attribute")) != "replace" || len(values) < 2 || len(values) > 3 {
		return "", "", false
	}
	read, readArgs := sourceCall(replace.ChildByFieldName("object"))
	if read == nil || s.text(read.ChildByFieldName("attribute")) != "read_text" || len(readArgs) != 0 {
		return "", "", false
	}
	readPaths := s.paths(read.ChildByFieldName("object"))
	if len(readPaths) != 1 || readPaths[0] != paths[0] {
		return "", "", false
	}
	old, okOld := s.literal(values[0])
	next, okNext := s.literal(values[1])
	if !okOld || !okNext {
		return "", "", false
	}
	count := -1
	if len(values) == 3 {
		var err error
		count, err = strconv.Atoi(s.text(values[2]))
		if err != nil {
			return "", "", false
		}
	}
	before, exists, err := liveDiffPreviewFile(paths[0])
	if err != nil || !exists {
		return "", "", false
	}
	// Bound expansion before allocating the replacement result.
	matches := strings.Count(before, old)
	if count >= 0 {
		matches = min(matches, count)
	}
	if growth := len(next) - len(old); growth > 0 && matches > (liveDiffPreviewFileLimit-len(before))/growth {
		return "", "", false
	}
	return paths[0], strings.Replace(before, old, next, count), true
}
