package router

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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
// these bytes as the observed outcome of a host command. A still-arriving
// content literal can expose complete target units without executing its script.
func liveDiffInterpreterWrite(ctx context.Context, stmt *syntax.Stmt, directory string, partial bool) ([]mekugi.ReviewFile, bool, error) {
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
		body, literal := liveDiffShellHeredoc(redirect, partial)
		if !literal {
			return nil, false, nil
		}
		input.stdin = body
	}
	source, script, reason := execProgramSource(input)
	if reason != "" || len(source) > maxExecProgramBytes {
		return nil, false, nil
	}
	files, _, err := liveDiffInterpreterSource(ctx, input, source, script, python, partial)
	return files, len(files) != 0, err
}

func liveDiffInterpreterSource(ctx context.Context, input execProviderInput, source, script string, python, partial bool) ([]mekugi.ReviewFile, bool, error) {
	if len(source) > maxExecProgramBytes {
		return nil, false, nil
	}
	language := codeModeJavaScriptLanguage
	if python {
		language = execPythonLanguage
	} else if strings.HasSuffix(script, ".ts") || input.identity == "deno" {
		language = execTypeScriptLanguage
	}
	canceled := func() bool { return ctx.Err() != nil || time.Now().After(input.deadline) }
	data := []byte(source)
	tree, err := parseExecSource(data, language, canceled)
	if err != nil {
		return nil, false, err
	}
	// A literal spanning the received end is still arriving. Zero means the
	// program is complete; no node starts before offset zero.
	arriving := uint(0)
	// Try single delimiters first: triple quotes can parse a short string as
	// adjacent literals, obscuring the still-arriving content literal.
	for _, closer := range []string{`")`, `')`, "`)", `""")`, `''')`, `"))`, `'))`, "`))", `"""))`, `'''))`} {
		if tree == nil || !partial || !tree.RootNode().HasError() {
			break
		}
		tree.Close()
		data = []byte(source + closer)
		tree, err = parseExecSource(data, language, canceled)
		if err != nil {
			return nil, false, err
		}
		arriving = uint(len(source))
	}

	if tree == nil {
		return nil, false, nil
	}
	defer tree.Close()
	editScript := python && liveDiffPythonEditIntent(tree.RootNode(), data, 0)
	if tree.RootNode().HasError() {
		return nil, editScript, nil
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
	texts := make(map[string]liveDiffPythonText)
	var textOrder []string
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
		if python && node.Kind() == "assignment" {
			left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
			if left == nil || left.Kind() != "identifier" {
				failure = errors.New("literal preview includes an unsupported assignment")
				return
			}
			if value, ok := scan.previewPythonText(ctx, right, texts, arriving, 0); ok {
				texts[scan.text(left)] = value
				textOrder = append(textOrder, scan.text(left))
				return
			}
			delete(texts, scan.text(left))
		}
		if python && (node.Kind() == "augmented_assignment" || node.Kind() == "delete_statement" || node.Kind() == "named_expression") {
			failure = errors.New("literal preview includes an unsupported buffer mutation")
			return
		}
		function, args := sourceCall(node)
		if function != nil {
			if strings.HasSuffix(scan.text(function), ".chdir") {
				failure = errors.New("literal preview does not predict working-directory changes")
				return
			}
			target, recognized := scan.literalWrite(ctx, function, args, texts, arriving)
			if recognized {
				path, content := target.path, target.content
				render := mekugi.RenderReviewFile
				if liveDiffArriving(node, arriving) {
					// Gate only the write still arriving, at its projected tip.
					// Earlier completed writes need no target-unit gate.
					tip := len(content)
					if target.tip > 0 {
						tip, render = target.tip, mekugi.RenderStreamingReviewFile
					}
					if !liveDiffSourceReady(ctx, path, content[:tip]) {
						failure = errors.New("target statement is still arriving")
						return
					}
				}
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
				files = append(files, render(beforePath, path, before, content))
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
		return nil, editScript, nil
	}
	// While source arrives, show the current replacement buffer even before
	// its final write statement. This is a prediction, never execution evidence.
	if partial {
		for _, name := range slices.Backward(textOrder) {
			value := texts[name]
			if value.path == "" || seen[value.path] {
				continue
			}
			render := mekugi.RenderReviewFile
			if value.tip > 0 {
				if !liveDiffSourceReady(ctx, value.path, value.content[:value.tip]) {
					return nil, editScript, nil
				}
				render = mekugi.RenderStreamingReviewFile
			}
			if len(files) >= 32 {
				return nil, false, nil
			}
			before, exists, err := liveDiffSourceRead(ctx, value.path, liveDiffPreviewFile)
			if err != nil || !exists || before == value.content {
				continue
			}
			files = append(files, render(value.path, value.path, before, value.content))
			seen[value.path] = true
		}
	}
	return files, len(files) != 0 || editScript, failure
}

// Keep an unfinished or unsupported edit script from replacing its target diff
// with the script's own source. Inspect syntax, not text inside comments/strings.
func liveDiffPythonEditIntent(node *sitter.Node, source []byte, depth int) bool {
	if node == nil || depth > 128 {
		return false
	}
	if function, _ := sourceCall(node); function != nil {
		if attribute := function.ChildByFieldName("attribute"); attribute != nil {
			switch string(source[attribute.StartByte():attribute.EndByte()]) {
			case "read_text", "write_text":
				return true
			}
		}
	}
	for i := range node.NamedChildCount() {
		if liveDiffPythonEditIntent(node.NamedChild(uint(i)), source, depth+1) {
			return true
		}
	}
	return false
}

func (s *execSourceScope) literalWrite(ctx context.Context, function *sitter.Node, args []*sitter.Node, texts map[string]liveDiffPythonText, arriving uint) (liveDiffPythonText, bool) {
	name := s.text(function)
	base := name[strings.LastIndexByte(name, '.')+1:]
	if alias := s.aliases[base]; alias != "" {
		base = alias
	}
	var paths []string
	var body *sitter.Node
	if !s.python {
		if (base != "writeFileSync" && base != "writeFile") || len(args) != 2 {
			return liveDiffPythonText{}, false
		}
		paths, body = s.paths(args[0]), args[1]
	} else {
		object := function.ChildByFieldName("object")
		if base == "write_text" && len(args) == 1 {
			paths, body = s.paths(object), args[0]
		} else if base == "write" && len(args) == 1 {
			open, values := sourceCall(object)
			if s.text(open) != "open" || len(values) != 2 {
				return liveDiffPythonText{}, false
			}
			mode, _ := s.literal(values[1])
			if mode != "w" {
				return liveDiffPythonText{}, false
			}
			paths, body = s.paths(values[0]), args[0]
		} else {
			return liveDiffPythonText{}, false
		}
	}
	if len(paths) != 1 {
		return liveDiffPythonText{}, false
	}
	if content, ok := s.literal(body); ok {
		return liveDiffPythonText{path: paths[0], content: content}, true
	}
	if !s.python {
		return liveDiffPythonText{}, false
	}
	value, ok := s.previewPythonText(ctx, body, texts, arriving, 0)
	if !ok || (value.path != "" && value.path != paths[0]) {
		return liveDiffPythonText{}, false
	}
	value.path = paths[0]
	return value, true
}

type liveDiffPythonText struct {
	path, content string
	// tip ends the still-arriving replacement text in content; zero when the
	// arriving literal is not a replacement in this value.
	tip int
}

func liveDiffArriving(node *sitter.Node, arriving uint) bool {
	return node.StartByte() < arriving && node.EndByte() > arriving
}

// Interpret only literal strings, same-file reads, and replacement chains.
// This does not run Python or resolve arbitrary expressions.
func (s *execSourceScope) previewPythonText(ctx context.Context, node *sitter.Node, texts map[string]liveDiffPythonText, arriving uint, depth int) (liveDiffPythonText, bool) {
	if node == nil || depth > 64 || ctx.Err() != nil || time.Now().After(s.input.deadline) {
		return liveDiffPythonText{}, false
	}
	if node.Kind() == "identifier" {
		value, ok := texts[s.text(node)]
		return value, ok
	}
	if content, ok := s.literal(node); ok {
		return liveDiffPythonText{content: content}, len(content) <= liveDiffPreviewFileLimit
	}
	replace, values := sourceCall(node)
	if replace != nil && s.text(replace.ChildByFieldName("attribute")) == "read_text" && len(values) == 0 {
		paths := s.paths(replace.ChildByFieldName("object"))
		if len(paths) != 1 || !filepath.IsAbs(paths[0]) {
			return liveDiffPythonText{}, false
		}
		content, exists, err := liveDiffSourceRead(ctx, paths[0], liveDiffPreviewFile)
		return liveDiffPythonText{path: paths[0], content: content}, exists && err == nil
	}
	if replace == nil || s.text(replace.ChildByFieldName("attribute")) != "replace" || len(values) < 2 || len(values) > 3 {
		return liveDiffPythonText{}, false
	}
	value, ok := s.previewPythonText(ctx, replace.ChildByFieldName("object"), texts, arriving, depth+1)
	if !ok {
		return liveDiffPythonText{}, false
	}
	old, okOld := s.literal(values[0])
	next, okNext := s.literal(values[1])
	if !okOld || !okNext {
		return liveDiffPythonText{}, false
	}
	count := -1
	if len(values) == 3 {
		var err error
		count, err = strconv.Atoi(s.text(values[2]))
		if err != nil {
			return liveDiffPythonText{}, false
		}
	}
	before := value.content
	// Bound expansion before allocating the replacement result.
	matches := strings.Count(before, old)
	if count >= 0 {
		matches = min(matches, count)
	}
	if growth := len(next) - len(old); growth > 0 && matches > (liveDiffPreviewFileLimit-len(before))/growth {
		return liveDiffPythonText{}, false
	}
	value.content = strings.Replace(before, old, next, count)
	if at := strings.Index(before, old); at >= 0 && liveDiffArriving(values[1], arriving) {
		value.tip = at + len(next)
	}
	return value, true
}
