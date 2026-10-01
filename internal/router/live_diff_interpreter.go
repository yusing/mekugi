package router

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
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
	tree, err := parseSourceTree(data, language, canceled)
	if err != nil {
		return nil, false, err
	}
	// A literal spanning the received end is still arriving. Zero means the
	// program is complete; no node starts before offset zero.
	arriving := uint(0)
	// Try single delimiters first: triple quotes can parse a short string as
	// adjacent literals, obscuring the still-arriving content literal.
	for _, closer := range []string{`"`, `'`, "`", `")`, `')`, "`)", `"""`, `'''`, `""")`, `''')`, `"))`, `'))`, "`))", `"""))`, `'''))`} {
		if tree == nil || !partial || !tree.RootNode().HasError() {
			break
		}
		tree.Close()
		data = []byte(source + closer)
		tree, err = parseSourceTree(data, language, canceled)
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
	scan := execSourceScope{input: input, source: data, script: script, python: python, vars: make(map[string][]string), segments: make(map[string][]string), texts: make(map[string]bool), assigned: make(map[string]int), aliases: make(map[string]string)}
	scan.walk(tree.RootNode())
	if python {
		// Bind Python paths as statements are visited, including each loop
		// iteration, rather than borrowing a later assignment from capture.
		clear(scan.vars)
		clear(scan.segments)
	} else {
		for name, count := range scan.assigned {
			if count > 1 {
				delete(scan.vars, name)
			}
		}
	}
	state := liveDiffPythonState{texts: make(map[string]liveDiffPythonText), handles: make(map[string]*liveDiffPythonHandle),
		ints: make(map[string]int), written: make(map[string]string), defs: make(map[string]liveDiffPythonDefinition), lists: make(map[string][]liveDiffPythonLoopItem), dicts: make(map[string][]liveDiffPythonLoopItem), reads: make(map[[2]uint]liveDiffPythonText)}
	clearBinding := func(name string) {
		scan.pythonClearBinding(&state, name)
	}
	var writes []liveDiffPredictedWrite
	var textOrder []string
	seen := make(map[string]bool)
	recordWrite := func(write liveDiffPredictedWrite) {
		state.written[write.path] = write.content
		if index := slices.IndexFunc(writes, func(w liveDiffPredictedWrite) bool { return w.path == write.path }); index >= 0 {
			writes[index] = write
		} else {
			writes = append(writes, write)
		}
		seen[write.path] = true
	}
	var failure error
	var failed *sitter.Node
	// calling is set while a helper call that spans the arriving point is
	// inlined, so writes in its body arrive with it.
	calling := false
	iterations := 0
	var visit func(*sitter.Node, int)
	var inline func(definition liveDiffPythonDefinition, call *sitter.Node, args []*sitter.Node, depth int) bool
	visit = func(node *sitter.Node, depth int) {
		if node == nil || failure != nil {
			return
		}
		defer func() {
			if failure != nil && failed == nil {
				failed = node
			}
		}()
		if depth > 128 || ctx.Err() != nil || time.Now().After(input.deadline) || len(writes) > 32 {
			failure = errors.New("literal preview capacity exceeded")
			return
		}
		if python {
			// Type derivation may inspect one expression more than once.
			// Each executed statement/iteration gets a fresh read cache, while
			// the handle's consumed position survives across statements.
			switch node.Kind() {
			case "expression_statement", "assignment", "augmented_assignment", "for_statement", "function_definition":
				clear(state.reads)
			}
			switch node.Kind() {
			case "for_statement":
				left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
				if left == nil || node.Child(0).Kind() == "async" || node.ChildByFieldName("alternative") != nil || liveDiffArriving(right, arriving) {
					failure = errors.New("literal preview includes unsupported control flow")
					return
				}
				var names []string
				unpack := left.Kind() != "identifier"
				if !unpack {
					names = []string{scan.text(left)}
				} else if left.Kind() == "pattern_list" || left.Kind() == "tuple_pattern" || left.Kind() == "list_pattern" {
					for i := range left.NamedChildCount() {
						target := left.NamedChild(uint(i))
						if target.Kind() == "comment" {
							continue
						}
						if target.Kind() != "identifier" {
							failure = errors.New("literal preview includes an unsupported loop target")
							return
						}
						names = append(names, scan.text(target))
					}
				}
				items, ok := scan.pythonLiteralList(ctx, right, &state, 0)
				if !ok || len(names) == 0 {
					failure = errors.New("literal preview includes unsupported iteration")
					return
				}
				for _, item := range items {
					if item.unpack != unpack || len(item.values) != len(names) {
						failure = errors.New("literal preview includes an unsupported loop target")
						return
					}
					iterations++
					if iterations > 256 {
						failure = errors.New("literal preview capacity exceeded")
						return
					}
					for i, name := range names {
						value := item.values[i]
						clearBinding(name)
						state.texts[name] = liveDiffPythonText{content: value}
						scan.vars[name] = []string{execProviderPath(input.cwd, value)}
						scan.segments[name] = []string{value}
					}
					visit(node.ChildByFieldName("body"), depth+1)
					if failure != nil {
						return
					}
				}
				return
			case "break_statement", "continue_statement":
				failure = errors.New("literal preview includes unsupported control flow")
				return
			case "if_statement":
				// A guard that only stops or reports leaves the edit path
				// unconditional, so the prediction assumes it passes.
				if !scan.pythonGuard(node) {
					failure = errors.New("literal preview includes unsupported control flow")
				}
				return
			case "assert_statement":
				if !scan.pythonPure(node, 0) {
					failure = errors.New("literal preview includes an unsupported assertion")
				}
				return
			case "with_statement":
				active := len(state.activeHandles)
				handles, ok := scan.pythonWith(node, &state)
				if !ok {
					failure = errors.New("literal preview includes an unsupported context manager")
					return
				}
				for _, handle := range handles {
					if handle.write {
						recordWrite(liveDiffPredictedWrite{path: handle.path})
					}
				}
				visit(node.ChildByFieldName("body"), depth+1)
				for _, handle := range handles {
					handle.closed = true
				}
				state.activeHandles = state.activeHandles[:active]
				return
			case "function_definition":
				name := node.ChildByFieldName("name")
				if parent := node.Parent(); parent == nil || parent.Kind() != "module" || name == nil {
					failure = errors.New("literal preview includes unsupported control flow")
					return
				}
				definition := liveDiffPythonDefinition{node: node, defaults: make(map[string]liveDiffPythonBinding)}
				parameters := node.ChildByFieldName("parameters")
				for i := range parameters.NamedChildCount() {
					parameter := parameters.NamedChild(uint(i))
					if parameter.Kind() != "default_parameter" {
						continue
					}
					value := parameter.ChildByFieldName("value")
					bound := scan.pythonBinding(ctx, value, &state, arriving)
					if !bound.hasText && !bound.hasInt && !bound.hasDictionary && !bound.hasList && len(bound.paths) != 1 {
						failure = errors.New("literal preview includes an unsupported helper default")
						return
					}
					definition.defaults[scan.text(parameter.ChildByFieldName("name"))] = bound
				}
				clearBinding(scan.text(name))
				state.defs[scan.text(name)] = definition
				return
			case "global_statement", "pass_statement":
				return
			}
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
			name := scan.text(left)
			paths := scan.paths(right)
			segments := scan.literalSegments(right, 0)
			dictionary, hasDictionary := scan.pythonDictionary(ctx, right, &state, 0)
			var items []liveDiffPythonLoopItem
			hasList := false
			if !hasDictionary {
				items, hasList = scan.pythonLiteralList(ctx, right, &state, 0)
			}
			value, hasText := scan.previewPythonText(ctx, right, &state, arriving, 0)
			var offset int
			hasInt := false
			if !hasText {
				offset, hasInt = scan.pythonInt(ctx, right, &state, 0)
			}
			clearBinding(name)
			scan.vars[name] = paths
			scan.segments[name] = segments
			if hasText {
				state.texts[name] = value
				if value.path == "" && value.tip == 0 && !liveDiffArriving(right, arriving) {
					scan.vars[name] = []string{execProviderPath(input.cwd, value.content)}
					scan.segments[name] = []string{value.content}
				}
				textOrder = append(textOrder, name)
				return
			}
			if hasInt {
				state.ints[name] = offset
				return
			}
			if hasDictionary {
				state.dicts[name] = dictionary
				return
			}
			if hasList {
				state.lists[name] = items
				return
			}
		}
		if python && node.Kind() == "augmented_assignment" {
			left, right := node.ChildByFieldName("left"), node.ChildByFieldName("right")
			if left == nil || left.Kind() != "identifier" || scan.text(node.ChildByFieldName("operator")) != "+=" {
				failure = errors.New("literal preview includes an unsupported buffer mutation")
				return
			}
			name := scan.text(left)
			before, known := state.texts[name]
			next, ok := scan.previewPythonText(ctx, right, &state, arriving, 0)
			joined, fits := liveDiffPythonConcat(before, next, liveDiffArriving(right, arriving))
			if !known || !ok || !fits {
				failure = errors.New("literal preview includes an unsupported buffer mutation")
				return
			}
			joined.path = before.path
			state.texts[name] = joined
			delete(scan.vars, name)
			delete(scan.segments, name)
			if joined.path == "" && joined.tip == 0 {
				scan.vars[name] = []string{execProviderPath(input.cwd, joined.content)}
				scan.segments[name] = []string{joined.content}
			}
			textOrder = append(textOrder, name)
			return
		}
		if python && (node.Kind() == "delete_statement" || node.Kind() == "named_expression") {
			failure = errors.New("literal preview includes an unsupported buffer mutation")
			return
		}
		function, args := sourceCall(node)
		if function != nil {
			if strings.HasSuffix(scan.text(function), ".chdir") {
				failure = errors.New("literal preview does not predict working-directory changes")
				return
			}
			if definition, exists := state.defs[scan.text(function)]; python && exists {
				if !inline(definition, node, args, depth) && failure == nil {
					failure = errors.New("literal preview includes an unsupported helper call")
				}
				return
			}
			target, recognized := scan.literalWrite(ctx, function, args, &state, arriving)
			if recognized {
				if !filepath.IsAbs(target.path) || len(target.content) > liveDiffPreviewFileLimit || !utf8.ValidString(target.content) {
					failure = errors.New("literal preview has unsupported writes")
					return
				}
				write := liveDiffPredictedWrite{path: target.path, content: target.content, tip: len(target.content)}
				// Inside a call still arriving, only writes of arriving text
				// are open; the helper's literal writes are complete.
				if liveDiffArriving(node, arriving) || calling && target.tip > 0 {
					// Only the write still arriving ends at its projected tip.
					write.arriving = true
					if target.tip > 0 {
						write.tip = target.tip
					}
				}
				// A later write replaces the file; reads after it see it.
				recordWrite(write)
				return
			}
			name := scan.text(function)
			base := name[strings.LastIndexByte(name, '.')+1:]
			if alias := scan.aliases[base]; alias != "" {
				base = alias
			}
			base = strings.TrimSuffix(base, "Sync")
			switch {
			case base == "Path" || base == "read_text" || base == "join" || base == "resolve":
				// Syntax-only path construction or a read used by this subset.
			case python && (base == "print" || liveDiffPythonPureCalls[base]):
				// Output and string inspection have no file effect.
			case base == "require":
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
	inline = func(definition liveDiffPythonDefinition, call *sitter.Node, args []*sitter.Node, depth int) bool {
		bindings, ok := scan.pythonParameters(definition.node, args)
		if !ok || depth > 32 {
			return false
		}
		// Evaluate every argument in the caller's scope before binding.
		values := make(map[string]liveDiffPythonBinding, len(bindings))
		names := slices.SortedFunc(maps.Keys(bindings), func(a, b string) int {
			left, right := bindings[a], bindings[b]
			if left == nil {
				if right == nil {
					return strings.Compare(a, b)
				}
				return -1
			}
			if right == nil {
				return 1
			}
			return cmp.Compare(left.StartByte(), right.StartByte())
		})
		for _, name := range names {
			value := bindings[name]
			if value == nil {
				values[name] = definition.defaults[name]
			} else {
				values[name] = scan.pythonBinding(ctx, value, &state, arriving)
			}
		}
		// Helper locals, parameters included, end with the call; names the
		// body declares global keep their new values.
		texts, handles, ints, vars, lists := maps.Clone(state.texts), maps.Clone(state.handles), maps.Clone(state.ints), maps.Clone(scan.vars), maps.Clone(state.lists)
		segments, defs := maps.Clone(scan.segments), maps.Clone(state.defs)
		dicts := maps.Clone(state.dicts)
		body := definition.node.ChildByFieldName("body")
		locals, globals := scan.pythonScope(body)
		for name := range locals {
			if !globals[name] {
				clearBinding(name)
			}
		}
		for name, bound := range values {
			clearBinding(name)
			if bound.hasText {
				state.texts[name] = bound.text
			}
			if bound.hasInt {
				state.ints[name] = bound.integer
			}
			if bound.hasDictionary {
				state.dicts[name] = bound.dictionary
			} else if bound.hasList {
				state.lists[name] = bound.items
			}
			if len(bound.paths) == 1 {
				scan.vars[name] = bound.paths
			}
			scan.segments[name] = bound.segments
		}
		wasCalling := calling
		calling = calling || liveDiffArriving(call, arriving)
		visit(body, depth+1)
		calling = wasCalling
		restoreScope(state.texts, texts, globals)
		restoreScope(state.handles, handles, globals)
		restoreScope(state.ints, ints, globals)
		restoreScope(scan.vars, vars, globals)
		restoreScope(state.lists, lists, globals)
		restoreScope(scan.segments, segments, globals)
		restoreScope(state.defs, defs, globals)
		restoreScope(state.dicts, dicts, globals)
		return failure == nil
	}
	visit(tree.RootNode(), 0)
	if failure != nil {
		if note, _ := ctx.Value(liveDiffFailureContext{}).(*error); note != nil {
			*note = fmt.Errorf("%w: %s %q", failure, failed.Kind(), scan.text(failed))
		}
		return nil, editScript, nil
	}
	// While source arrives, show the current replacement buffer even before
	// its final write statement. This is a prediction, never execution evidence.
	if partial {
		for _, name := range slices.Backward(textOrder) {
			value := state.texts[name]
			if value.path == "" || seen[value.path] {
				continue
			}
			if len(writes) >= 32 {
				return nil, false, nil
			}
			before, exists, err := liveDiffSourceRead(ctx, value.path, liveDiffPreviewFile)
			if err != nil || !exists || before == value.content {
				continue
			}
			write := liveDiffPredictedWrite{path: value.path, content: value.content, tip: len(value.content)}
			if value.tip > 0 {
				write.tip, write.arriving = value.tip, true
			}
			writes = append(writes, write)
			seen[value.path] = true
		}
	}
	if len(writes) == 0 {
		return nil, editScript, nil
	}
	patch, baseline, err := liveDiffPredictedPatch(ctx, writes)
	if err != nil {
		return nil, true, nil
	}
	files, err := stockPatchReviewPreview(baseline, "", patch, !partial)
	if err != nil {
		return nil, true, nil
	}
	return files, true, nil
}

// liveDiffFailureContext carries an *error that receives why an interpreter
// prediction stopped, for coverage diagnostics. Previews never read it.
type liveDiffFailureContext struct{}

// liveDiffPredictedWrite is one file's predicted content. An arriving write
// is shown only through tip.
type liveDiffPredictedWrite struct {
	path, content string
	tip           int
	arriving      bool
}

// liveDiffPredictedPatch expresses predicted file contents as a stock patch,
// so interpreter edits stream through the same projection as apply_patch.
// The write still arriving comes last and ends at its tip, leaving the patch
// open like a stock patch whose input is still streaming. The returned context
// projects the patch against the same contents the prediction started from.
func liveDiffPredictedPatch(ctx context.Context, writes []liveDiffPredictedWrite) (string, context.Context, error) {
	slices.SortStableFunc(writes, func(a, b liveDiffPredictedWrite) int {
		switch {
		case a.arriving == b.arriving:
			return 0
		case a.arriving:
			return 1
		}
		return -1
	})
	// A patch stays open only at its end, so earlier arriving writes show
	// their current prediction as complete.
	for i := range writes[:max(len(writes)-1, 0)] {
		writes[i].arriving = false
	}
	var patch strings.Builder
	patch.WriteString("*** Begin Patch\n")
	open := false
	baseline := &liveDiffSources{capturedOnly: true, files: make(map[liveDiffSourceKey]liveDiffSource)}
	projector := reflect.ValueOf(readNativePatchFile).Pointer()
	for _, write := range writes {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		if strings.ContainsAny(write.path, "\r\n") {
			return "", nil, errors.New("predicted path cannot be expressed as a patch")
		}
		before, exists, err := liveDiffSourceRead(ctx, write.path, liveDiffPreviewFile)
		if err != nil {
			return "", nil, err
		}
		before, write = liveDiffPatchText(before, write)
		baseline.files[liveDiffSourceKey{projector, write.path}] = liveDiffSource{before, exists}
		open = open || write.arriving
		if !exists {
			patch.WriteString("*** Add File: " + write.path + "\n")
			for line := range strings.SplitAfterSeq(write.content[:write.tip], "\n") {
				if line != "" {
					patch.WriteString("+" + line)
				}
			}
			continue
		}
		if before == write.content {
			continue
		}
		patch.WriteString("*** Update File: " + write.path + "\n")
		after, tip := write.content, write.tip
		if !write.arriving && (after == "\n" || strings.HasSuffix(after, "\n\n")) {
			// Stock updates treat their final empty row as the LF sentinel.
			// Encode a separate sentinel so an intentional trailing blank
			// line survives projection, without changing the stock parser.
			after += "\n"
			tip = len(after)
		}
		liveDiffPatchHunks(&patch, before, after, tip, write.arriving)
	}
	if !open {
		patch.WriteString("*** End Patch\n")
	}
	return patch.String(), context.WithValue(ctx, liveDiffSourcesContext{}, baseline), nil
}

// liveDiffPatchText adapts a prediction to what a patch can express, for
// display only: patch lines drop carriage returns, and a final line always
// ends with a newline. A change only to the final newline shows no rows.
func liveDiffPatchText(before string, write liveDiffPredictedWrite) (string, liveDiffPredictedWrite) {
	if strings.Contains(before, "\r") || strings.Contains(write.content, "\r") {
		write.tip -= strings.Count(write.content[:write.tip], "\r")
		before, write.content = strings.ReplaceAll(before, "\r", ""), strings.ReplaceAll(write.content, "\r", "")
	}
	if before != "" && !strings.HasSuffix(before, "\n") {
		before += "\n"
	}
	if !write.arriving && write.content != "" && !strings.HasSuffix(write.content, "\n") {
		write.content += "\n"
		write.tip = len(write.content)
	}
	return before, write
}

// liveDiffPatchHunks writes minimal hunks from before to after. Leading
// context grows until each hunk's old lines first match where they belong,
// because stock projection takes the first match. An arriving write stops at
// its tip: removed rows of the arriving region precede its added rows.
func liveDiffPatchHunks(patch *strings.Builder, before, after string, tip int, arriving bool) {
	split := func(text string) []string {
		if text == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	}
	a, b := split(before), split(after)
	tipLine, partial := len(b), ""
	if arriving {
		tipLine = strings.Count(after[:tip], "\n")
		partial = after[strings.LastIndexByte(after[:tip], '\n')+1 : tip]
	}
	position := 0
	for _, group := range difflib.NewMatcher(a, b).GetGroupedOpCodes(3) {
		var lines []string
		var old []string
		stop := false
		emit := func(prefix byte, text string) {
			lines = append(lines, string(prefix)+text)
			if prefix != '+' {
				old = append(old, text)
			}
		}
		for _, op := range group {
			if op.J1 > tipLine || op.J1 == tipLine && op.Tag == 'e' {
				stop = true
				break
			}
			switch op.Tag {
			case 'e':
				for j := op.J1; j < min(op.J2, tipLine); j++ {
					emit(' ', b[j])
				}
				stop = op.J2 > tipLine
			case 'd':
				for _, line := range a[op.I1:op.I2] {
					emit('-', line)
				}
			default:
				for _, line := range a[op.I1:op.I2] {
					emit('-', line)
				}
				for j := op.J1; j < min(op.J2, tipLine); j++ {
					emit('+', b[j])
				}
				if op.J2 > tipLine || op.J2 == tipLine && partial != "" {
					if partial != "" {
						lines = append(lines, "+"+partial)
					}
					stop = true
				}
			}
			if stop {
				break
			}
		}
		if len(lines) != 0 {
			start := group[0].I1
			if len(old) == 0 && start == position && len(a) != 0 {
				// Stock projection appends a hunk without old lines at the end
				// of the file; an insertion cut before its trailing context
				// has no position yet.
				break
			}
			for start > position && liveDiffFirstMatch(a, old, position) != start {
				start--
				old = append([]string{a[start]}, old...)
				lines = append([]string{" " + a[start]}, lines...)
			}
			patch.WriteString("@@\n")
			for _, line := range lines {
				patch.WriteString(line + "\n")
			}
			position = start + len(old)
		}
		if stop {
			break
		}
	}
	if arriving {
		// The arriving tip is not a complete line until the source says so.
		text := patch.String()
		if partial != "" && strings.HasSuffix(text, "+"+partial+"\n") {
			patch.Reset()
			patch.WriteString(strings.TrimSuffix(text, "\n"))
		}
	}
}

func liveDiffFirstMatch(lines, old []string, from int) int {
	for i := from; i+len(old) <= len(lines); i++ {
		if slices.Equal(lines[i:i+len(old)], old) {
			return i
		}
	}
	return -1
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

// liveDiffPythonSetup reports whether received Python source is still only the
// preamble an edit script shares with ordinary source: imports, docstrings,
// and literal or path assignments. Any other statement makes it ordinary
// source, which streams like any other file.
func liveDiffPythonSetup(ctx context.Context, source string) bool {
	data := []byte(source)
	tree, err := parseSourceTree(data, execPythonLanguage, func() bool { return ctx.Err() != nil })
	if err != nil || tree == nil {
		return true
	}
	defer tree.Close()
	var setup func(*sitter.Node, int) bool
	setup = func(block *sitter.Node, depth int) bool {
		if block == nil || depth > 8 {
			return false
		}
		for i := range block.NamedChildCount() {
			node := block.NamedChild(i)
			switch node.Kind() {
			case "import_statement", "import_from_statement", "future_import_statement", "comment", "ERROR":
				// An unfinished statement cannot yet show edit intent.
				continue
			case "with_statement":
				// `with open(path) as f: text = f.read()` only reads.
				if setup(node.ChildByFieldName("body"), depth+1) || node.ChildByFieldName("body") == nil {
					continue
				}
			case "expression_statement":
				if node.NamedChildCount() != 1 {
					return false
				}
				child := node.NamedChild(0)
				if child.Kind() == "string" {
					continue
				}
				if function, _ := sourceCall(child); function != nil && strings.HasPrefix(string(data[function.StartByte():function.EndByte()]), "sys.path.") {
					continue
				}
				if child.Kind() == "assignment" && liveDiffPythonPathValue(child.ChildByFieldName("right"), data, 0) {
					continue
				}
			}
			return false
		}
		return true
	}
	return setup(tree.RootNode(), 0)
}

func liveDiffPythonPathValue(node *sitter.Node, data []byte, depth int) bool {
	if node == nil || depth > 16 {
		return false
	}
	switch node.Kind() {
	case "string", "concatenated_string", "identifier":
		return true
	case "attribute":
		name := node.ChildByFieldName("attribute")
		return name != nil && string(data[name.StartByte():name.EndByte()]) == "parent" &&
			liveDiffPythonPathValue(node.ChildByFieldName("object"), data, depth+1)
	case "binary_operator":
		operator := node.ChildByFieldName("operator")
		return operator != nil && operator.Kind() == "/" &&
			liveDiffPythonPathValue(node.ChildByFieldName("left"), data, depth+1) &&
			liveDiffPythonPathValue(node.ChildByFieldName("right"), data, depth+1)
	}
	function, _ := sourceCall(node)
	if function == nil {
		return false
	}
	name := string(data[function.StartByte():function.EndByte()])
	switch name[strings.LastIndexByte(name, '.')+1:] {
	case "Path", "PurePath", "join", "resolve", "absolute", "expanduser", "abspath", "realpath", "dirname", "getcwd", "cwd", "home", "open", "read":
		return true
	}
	return false
}

func (s *execSourceScope) literalWrite(ctx context.Context, function *sitter.Node, args []*sitter.Node, state *liveDiffPythonState, arriving uint) (liveDiffPythonText, bool) {
	name := s.text(function)
	base := name[strings.LastIndexByte(name, '.')+1:]
	if alias := s.aliases[base]; alias != "" {
		base = alias
	}
	var paths []string
	var body *sitter.Node
	var newline *string
	var writing *liveDiffPythonHandle
	if !s.python {
		if (base != "writeFileSync" && base != "writeFile") || len(args) != 2 {
			return liveDiffPythonText{}, false
		}
		paths, body = s.paths(args[0]), args[1]
	} else {
		positional, ok := liveDiffPythonArguments(s, args, "encoding", "errors", "newline")
		if !ok || len(positional) != 1 {
			return liveDiffPythonText{}, false
		}
		object := function.ChildByFieldName("object")
		switch {
		case base == "write_text":
			paths = s.paths(object)
			if newline, ok = s.pythonNewline(args); !ok {
				return liveDiffPythonText{}, false
			}
		case base == "write" && object != nil && object.Kind() == "identifier":
			handle, found := state.handles[s.text(object)]
			if !found || !handle.write || handle.closed || len(args) != 1 {
				return liveDiffPythonText{}, false
			}
			paths, newline = []string{handle.path}, handle.newline
			writing = handle
		case base == "write":
			handle, ok := s.pythonOpen(object)
			if !ok || !handle.write || len(args) != 1 {
				return liveDiffPythonText{}, false
			}
			paths, newline = []string{handle.path}, handle.newline
		default:
			return liveDiffPythonText{}, false
		}
		body = positional[0]
	}
	if len(paths) != 1 {
		return liveDiffPythonText{}, false
	}
	if s.python && writing == nil && liveDiffPythonOpenConflict(state, paths[0], true) {
		return liveDiffPythonText{}, false
	}
	content, literal := s.literal(body)
	if s.python {
		content, literal = s.pythonTextLiteral(body)
	}
	value := liveDiffPythonText{content: content}
	if !literal {
		if !s.python {
			return liveDiffPythonText{}, false
		}
		var ok bool
		value, ok = s.previewPythonText(ctx, body, state, arriving, 0)
		if !ok || (value.path != "" && value.path != paths[0]) {
			return liveDiffPythonText{}, false
		}
	}
	if newline != nil && *newline != "" && *newline != "\n" {
		if value.tip > 0 {
			value.tip = len(strings.ReplaceAll(value.content[:value.tip], "\n", *newline))
		}
		value.content = strings.ReplaceAll(value.content, "\n", *newline)
	}
	if writing != nil {
		joined, ok := liveDiffPythonConcat(writing.content, value, liveDiffArriving(body, arriving) || value.arriving)
		if !ok {
			return liveDiffPythonText{}, false
		}
		value = joined
		writing.content = joined
	}
	value.path = paths[0]
	return value, true
}

type liveDiffPythonText struct {
	path, content string
	// tip ends the still-arriving replacement text in content; zero when the
	// arriving literal is not a replacement in this value.
	tip int
	// arriving marks a helper argument bound to the still-arriving literal.
	arriving bool
}

// restoreScope ends a helper call: names other than its globals return to
// their values before the call.
func restoreScope[V any](current, saved map[string]V, globals map[string]bool) {
	for name := range current {
		if _, existed := saved[name]; !existed && !globals[name] {
			delete(current, name)
		}
	}
	for name, value := range saved {
		if !globals[name] {
			current[name] = value
		}
	}
}

// pythonParameters maps a helper's parameters to the call's argument
// expressions, or to their defaults.
func (s *execSourceScope) pythonParameters(definition *sitter.Node, args []*sitter.Node) (map[string]*sitter.Node, bool) {
	parameters := definition.ChildByFieldName("parameters")
	if parameters == nil {
		return nil, false
	}
	var names []string
	defaults := make(map[string]*sitter.Node)
	for i := range parameters.NamedChildCount() {
		parameter := parameters.NamedChild(uint(i))
		switch parameter.Kind() {
		case "identifier":
			names = append(names, s.text(parameter))
		case "default_parameter":
			name := s.text(parameter.ChildByFieldName("name"))
			names = append(names, name)
			defaults[name] = parameter.ChildByFieldName("value")
		case "comment":
		default:
			return nil, false
		}
	}
	bindings := make(map[string]*sitter.Node, len(names))
	position := 0
	for _, arg := range args {
		switch arg.Kind() {
		case "comment":
		case "keyword_argument":
			name := s.text(arg.ChildByFieldName("name"))
			if !slices.Contains(names, name) || bindings[name] != nil {
				return nil, false
			}
			bindings[name] = arg.ChildByFieldName("value")
		case "list_splat", "dictionary_splat":
			return nil, false
		default:
			if position >= len(names) || bindings[names[position]] != nil {
				return nil, false
			}
			bindings[names[position]] = arg
			position++
		}
	}
	for _, name := range names {
		if bindings[name] == nil {
			if defaults[name] == nil {
				return nil, false
			}
			// Defaults were evaluated when the definition was visited, not in
			// the caller's current scope. nil selects that captured binding.
			bindings[name] = nil
		}
	}
	return bindings, true
}

// pythonScope finds static local bindings and global declarations throughout
// a helper body. A later assignment makes a name local even before it runs.
func (s *execSourceScope) pythonScope(body *sitter.Node) (locals, globals map[string]bool) {
	locals, globals = make(map[string]bool), make(map[string]bool)
	var target func(*sitter.Node)
	target = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Kind() == "identifier" {
			locals[s.text(node)] = true
		} else if node.Kind() == "pattern_list" || node.Kind() == "tuple_pattern" || node.Kind() == "list_pattern" || node.Kind() == "as_pattern_target" {
			for i := range node.NamedChildCount() {
				target(node.NamedChild(uint(i)))
			}
		}
	}
	var visit func(*sitter.Node, int)
	visit = func(node *sitter.Node, depth int) {
		if node == nil || depth > 128 || node.Kind() == "function_definition" {
			return
		}
		switch node.Kind() {
		case "global_statement":
			for i := range node.NamedChildCount() {
				globals[s.text(node.NamedChild(uint(i)))] = true
			}
		case "assignment", "augmented_assignment", "for_statement":
			target(node.ChildByFieldName("left"))
		case "as_pattern":
			target(node.ChildByFieldName("alias"))
		}
		for i := range node.NamedChildCount() {
			visit(node.NamedChild(uint(i)), depth+1)
		}
	}
	visit(body, 0)
	return locals, globals
}

// pythonInt resolves an integer from literals, bound integers, `len` of a
// string, and `index`/`find` positions in a known text, with + and -.
// Positions count Unicode codepoints, matching Python's string indexing.
func (s *execSourceScope) pythonInt(ctx context.Context, node *sitter.Node, state *liveDiffPythonState, depth int) (int, bool) {
	if node == nil || depth > 16 {
		return 0, false
	}
	switch node.Kind() {
	case "integer":
		value, err := strconv.Atoi(s.text(node))
		return value, err == nil
	case "identifier":
		value, found := state.ints[s.text(node)]
		return value, found
	case "parenthesized_expression":
		return s.pythonInt(ctx, node.NamedChild(0), state, depth+1)
	case "unary_operator":
		value, ok := s.pythonInt(ctx, node.ChildByFieldName("argument"), state, depth+1)
		if operator := node.ChildByFieldName("operator"); !ok || operator == nil || s.text(operator) != "-" {
			return 0, false
		}
		return -value, true
	case "binary_operator":
		left, okLeft := s.pythonInt(ctx, node.ChildByFieldName("left"), state, depth+1)
		right, okRight := s.pythonInt(ctx, node.ChildByFieldName("right"), state, depth+1)
		if !okLeft || !okRight {
			return 0, false
		}
		switch s.text(node.ChildByFieldName("operator")) {
		case "+":
			return left + right, true
		case "-":
			return left - right, true
		}
		return 0, false
	}
	function, args := sourceCall(node)
	if function == nil {
		return 0, false
	}
	positional, ok := liveDiffPythonArguments(s, args)
	if !ok {
		return 0, false
	}
	if s.text(function) == "len" && len(positional) == 1 {
		if text, ok := s.pythonString(ctx, positional[0], state, depth+1); ok {
			return utf8.RuneCountInString(text), true
		}
		if items, ok := s.pythonLiteralList(ctx, positional[0], state, depth+1); ok {
			return len(items), true
		}
		return 0, false
	}
	if function.Kind() != "attribute" || len(positional) < 1 || len(positional) > 3 {
		return 0, false
	}
	method := s.text(function.ChildByFieldName("attribute"))
	if method != "index" && method != "find" && method != "rindex" && method != "rfind" && method != "count" {
		return 0, false
	}
	text, ok := s.pythonString(ctx, function.ChildByFieldName("object"), state, depth+1)
	needle, okNeedle := s.pythonString(ctx, positional[0], state, depth+1)
	if !ok || !okNeedle {
		return 0, false
	}
	runes := []rune(text)
	start, stop := 0, len(runes)
	for i, arg := range positional[1:] {
		bound, ok := s.pythonInt(ctx, arg, state, depth+1)
		if !ok {
			return 0, false
		}
		if bound < 0 {
			bound += len(runes)
		}
		if i == 0 {
			start = max(bound, 0)
		} else {
			stop = min(max(bound, 0), len(runes))
		}
	}
	at := -1
	if start <= stop && start <= len(runes) {
		window := string(runes[start:stop])
		if method == "count" {
			return strings.Count(window, needle), true
		}
		at = strings.Index(window, needle)
		if strings.HasPrefix(method, "r") {
			at = strings.LastIndex(window, needle)
		}
		if at >= 0 {
			at = start + utf8.RuneCountInString(window[:at])
		}
	} else if method == "count" {
		return 0, true
	}
	if at < 0 && strings.HasSuffix(method, "index") {
		// index raises, so the script stops before writing.
		return 0, false
	}
	return at, true
}

// pythonSlice resolves text[start:stop] bounds with Python semantics.
func (s *execSourceScope) pythonSlice(ctx context.Context, node *sitter.Node, state *liveDiffPythonState, length int) (int, int, bool) {
	bounds := [2]*sitter.Node{}
	part := 0
	for i := range node.ChildCount() {
		child := node.Child(uint(i))
		if s.text(child) == ":" {
			part++
			if part > 1 {
				return 0, 0, false
			}
			continue
		}
		if child.IsNamed() {
			bounds[part] = child
		}
	}
	if part != 1 {
		return 0, 0, false
	}
	resolve := func(bound *sitter.Node, fallback int) (int, bool) {
		if bound == nil {
			return fallback, true
		}
		value, ok := s.pythonInt(ctx, bound, state, 0)
		if value < 0 {
			value += length
		}
		return min(max(value, 0), length), ok
	}
	start, okStart := resolve(bounds[0], 0)
	stop, okStop := resolve(bounds[1], length)
	return start, max(start, stop), okStart && okStop
}

// liveDiffPythonState holds the literal text buffers and open file handles
// a prediction has derived so far.
type liveDiffPythonState struct {
	texts   map[string]liveDiffPythonText
	handles map[string]*liveDiffPythonHandle
	// Context resources outlive rebinding and helper-local name shadowing.
	activeHandles []*liveDiffPythonHandle
	// A read expression is evaluated once during a statement's type probes.
	reads map[[2]uint]liveDiffPythonText
	ints  map[string]int
	// written holds each predicted file's content, which later reads see.
	written map[string]string
	// defs are top-level helper functions, inlined at their calls.
	defs map[string]liveDiffPythonDefinition
	// lists contain only derived strings, never pending expressions to evaluate.
	lists map[string][]liveDiffPythonLoopItem
	// dicts preserve immutable string pairs in Python insertion order.
	dicts map[string][]liveDiffPythonLoopItem
}

type liveDiffPythonLoopItem struct {
	values []string
	unpack bool
}

func liveDiffPythonConcat(left, right liveDiffPythonText, arriving bool) (liveDiffPythonText, bool) {
	if len(left.content)+len(right.content) > liveDiffPreviewFileLimit {
		return liveDiffPythonText{}, false
	}
	joined := liveDiffPythonText{content: left.content + right.content}
	switch {
	case right.tip > 0:
		joined.tip = len(left.content) + right.tip
	case arriving || right.arriving:
		joined.tip = len(joined.content)
	case left.tip > 0:
		joined.tip = left.tip
	}
	return joined, true
}

type liveDiffPythonHandle struct {
	path   string
	write  bool
	closed bool
	// nil enables universal-newline translation on reads. Explicit newline
	// strings preserve input and control LF translation on writes.
	newline *string
	// Consecutive writes on this open handle advance its position. A new
	// truncating open gets a new handle, independently of prior file writes.
	content  liveDiffPythonText
	consumed bool
}

// Concurrent handles to one file need buffering and arbitrary file positions,
// which this preview does not simulate. Sequential closed handles are distinct.
func liveDiffPythonOpenConflict(state *liveDiffPythonState, path string, write bool) bool {
	for _, handle := range state.activeHandles {
		if !handle.closed && handle.path == path && (write || handle.write) {
			return true
		}
	}
	return false
}

func (s *execSourceScope) pythonClearBinding(state *liveDiffPythonState, name string) {
	delete(state.texts, name)
	delete(state.ints, name)
	delete(state.handles, name)
	delete(state.lists, name)
	delete(state.dicts, name)
	delete(state.defs, name)
	delete(s.vars, name)
	delete(s.segments, name)
}

type liveDiffPythonDefinition struct {
	node     *sitter.Node
	defaults map[string]liveDiffPythonBinding
}

type liveDiffPythonBinding struct {
	text          liveDiffPythonText
	hasText       bool
	paths         []string
	segments      []string
	integer       int
	hasInt        bool
	items         []liveDiffPythonLoopItem
	hasList       bool
	dictionary    []liveDiffPythonLoopItem
	hasDictionary bool
}

func (s *execSourceScope) pythonBinding(ctx context.Context, value *sitter.Node, state *liveDiffPythonState, arriving uint) liveDiffPythonBinding {
	var bound liveDiffPythonBinding
	bound.text, bound.hasText = s.previewPythonText(ctx, value, state, arriving, 0)
	if bound.hasText && liveDiffArriving(value, arriving) {
		bound.text.arriving = true
	}
	bound.paths, bound.segments = s.paths(value), s.literalSegments(value, 0)
	bound.integer, bound.hasInt = s.pythonInt(ctx, value, state, 0)
	bound.dictionary, bound.hasDictionary = s.pythonDictionary(ctx, value, state, 0)
	if !bound.hasDictionary {
		bound.items, bound.hasList = s.pythonLiteralList(ctx, value, state, 0)
	}
	return bound
}

// liveDiffPythonPureCalls inspect strings without effects, as in guards
// that check a replacement target before writing.
var liveDiffPythonPureCalls = map[string]bool{
	"len": true, "count": true, "startswith": true, "endswith": true, "find": true, "rfind": true,
	"index": true, "rindex": true, "strip": true, "lstrip": true, "rstrip": true, "splitlines": true,
	"split": true, "lower": true, "upper": true, "isinstance": true, "str": true, "repr": true,
}

func liveDiffArriving(node *sitter.Node, arriving uint) bool {
	return node.StartByte() < arriving && node.EndByte() > arriving
}

// liveDiffPythonArguments separates positional arguments from keywords,
// accepting only the named keywords, whose values do not affect content.
func liveDiffPythonArguments(s *execSourceScope, args []*sitter.Node, keywords ...string) ([]*sitter.Node, bool) {
	var positional []*sitter.Node
	for _, arg := range args {
		if arg.Kind() == "comment" {
			continue
		}
		if arg.Kind() != "keyword_argument" {
			positional = append(positional, arg)
			continue
		}
		if !slices.Contains(keywords, s.text(arg.ChildByFieldName("name"))) {
			return nil, false
		}
	}
	return positional, true
}

// pythonOpen resolves open(path[, mode]) and Path.open([mode]) to one
// absolute path and whether the text mode truncates for writing.
func (s *execSourceScope) pythonOpen(node *sitter.Node) (liveDiffPythonHandle, bool) {
	function, args := sourceCall(node)
	if function == nil {
		return liveDiffPythonHandle{}, false
	}
	var paths []string
	var modeNode *sitter.Node
	positional, ok := liveDiffPythonArguments(s, args, "encoding", "errors", "newline", "mode")
	newline, known := s.pythonNewline(args)
	if !ok || !known {
		return liveDiffPythonHandle{}, false
	}
	switch {
	case s.text(function) == "open" && (len(positional) == 1 || len(positional) == 2):
		paths = s.paths(positional[0])
		if len(positional) == 2 {
			modeNode = positional[1]
		}
	case function.Kind() == "attribute" && s.text(function.ChildByFieldName("attribute")) == "open" && len(positional) <= 1:
		paths = s.paths(function.ChildByFieldName("object"))
		if len(positional) == 1 {
			modeNode = positional[0]
		}
	default:
		return liveDiffPythonHandle{}, false
	}
	for _, arg := range args {
		if arg.Kind() == "keyword_argument" && s.text(arg.ChildByFieldName("name")) == "mode" {
			modeNode = arg.ChildByFieldName("value")
		}
	}
	mode := "r"
	if modeNode != nil {
		if mode, ok = s.literal(modeNode); !ok {
			return liveDiffPythonHandle{}, false
		}
	}
	if len(paths) != 1 || !filepath.IsAbs(paths[0]) {
		return liveDiffPythonHandle{}, false
	}
	switch mode {
	case "r", "rt":
		return liveDiffPythonHandle{path: paths[0], newline: newline}, true
	case "w", "wt":
		return liveDiffPythonHandle{path: paths[0], write: true, newline: newline}, true
	}
	return liveDiffPythonHandle{}, false
}

// pythonWith binds the handles of `with open(...) as name` items.
func (s *execSourceScope) pythonWith(node *sitter.Node, state *liveDiffPythonState) ([]*liveDiffPythonHandle, bool) {
	start := len(state.activeHandles)
	for i := range node.NamedChildCount() {
		clause := node.NamedChild(uint(i))
		if clause.Kind() != "with_clause" {
			continue
		}
		for j := range clause.NamedChildCount() {
			item := clause.NamedChild(uint(j)).ChildByFieldName("value")
			if item == nil || item.Kind() != "as_pattern" || item.NamedChildCount() == 0 {
				return nil, false
			}
			target := item.ChildByFieldName("alias")
			if target != nil && target.Kind() == "as_pattern_target" && target.NamedChildCount() == 1 {
				target = target.NamedChild(0)
			}
			handle, ok := s.pythonOpen(item.NamedChild(0))
			if !ok || target == nil || target.Kind() != "identifier" || liveDiffPythonOpenConflict(state, handle.path, handle.write) {
				return nil, false
			}
			name := s.text(target)
			s.pythonClearBinding(state, name)
			state.handles[name] = &handle
			state.activeHandles = append(state.activeHandles, &handle)
		}
	}
	return state.activeHandles[start:], true
}

// pythonGuard reports an if statement whose branches only stop or report,
// with conditions that inspect strings without effects.
func (s *execSourceScope) pythonGuard(node *sitter.Node) bool {
	for _, field := range []string{"condition", "consequence"} {
		if node.ChildByFieldName(field) == nil {
			return false
		}
	}
	var branch func(*sitter.Node) bool
	branch = func(clause *sitter.Node) bool {
		if condition := clause.ChildByFieldName("condition"); condition != nil && !s.pythonPure(condition, 0) {
			return false
		}
		body := clause.ChildByFieldName("consequence")
		if body == nil {
			body = clause.ChildByFieldName("body")
		}
		if body == nil {
			return false
		}
		for i := range body.NamedChildCount() {
			statement := body.NamedChild(uint(i))
			switch statement.Kind() {
			case "comment", "pass_statement":
				continue
			case "raise_statement":
				if s.pythonPure(statement, 0) || s.pythonStop(statement.NamedChild(0)) {
					continue
				}
			case "expression_statement":
				if statement.NamedChildCount() == 1 && s.pythonStop(statement.NamedChild(0)) {
					continue
				}
			}
			return false
		}
		return true
	}
	if !branch(node) {
		return false
	}
	for i := range node.NamedChildCount() {
		if clause := node.NamedChild(uint(i)); (clause.Kind() == "elif_clause" || clause.Kind() == "else_clause") && !branch(clause) {
			return false
		}
	}
	return true
}

// pythonStop matches exits, error constructors, and printing whose
// arguments have no effect.
func (s *execSourceScope) pythonStop(node *sitter.Node) bool {
	function, args := sourceCall(node)
	if function == nil {
		return false
	}
	switch s.text(function) {
	case "print", "exit", "quit", "sys.exit", "SystemExit", "RuntimeError", "ValueError", "AssertionError", "Exception":
	default:
		return false
	}
	for _, arg := range args {
		if !s.pythonPure(arg, 0) {
			return false
		}
	}
	return true
}

// pythonPure reports syntax that calls only string inspection.
func (s *execSourceScope) pythonPure(node *sitter.Node, depth int) bool {
	if node == nil || depth > 64 {
		return node == nil
	}
	switch node.Kind() {
	case "named_expression", "assignment", "augmented_assignment", "lambda", "await", "yield":
		return false
	}
	if function, _ := sourceCall(node); function != nil {
		name := s.text(function)
		if !liveDiffPythonPureCalls[name[strings.LastIndexByte(name, '.')+1:]] {
			return false
		}
	}
	for i := range node.NamedChildCount() {
		if !s.pythonPure(node.NamedChild(uint(i)), depth+1) {
			return false
		}
	}
	return true
}

// pythonString resolves a complete known string, including source-derived
// expressions. Arriving text cannot decide a search operand or offset.
func (s *execSourceScope) pythonString(ctx context.Context, node *sitter.Node, state *liveDiffPythonState, depth int) (string, bool) {
	value, ok := s.previewPythonText(ctx, node, state, 0, depth+1)
	return value.content, ok && value.tip == 0 && !value.arriving
}

// Interpret only literal strings, same-file reads, and replacement chains.
// This does not run Python or resolve arbitrary expressions.
func (s *execSourceScope) previewPythonText(ctx context.Context, node *sitter.Node, state *liveDiffPythonState, arriving uint, depth int) (liveDiffPythonText, bool) {
	if node == nil || depth > 64 || ctx.Err() != nil || time.Now().After(s.input.deadline) {
		return liveDiffPythonText{}, false
	}
	if node.Kind() == "identifier" {
		value, ok := state.texts[s.text(node)]
		return value, ok
	}
	if node.Kind() == "parenthesized_expression" && node.NamedChildCount() == 1 {
		return s.previewPythonText(ctx, node.NamedChild(0), state, arriving, depth+1)
	}
	if content, ok := s.pythonTextLiteral(node); ok {
		return liveDiffPythonText{content: content}, len(content) <= liveDiffPreviewFileLimit
	}
	switch node.Kind() {
	case "subscript":
		if dictionary, ok := s.pythonDictionary(ctx, node.ChildByFieldName("value"), state, depth+1); ok {
			key, known := s.pythonString(ctx, node.ChildByFieldName("subscript"), state, depth+1)
			if known {
				for _, item := range dictionary {
					if item.values[0] == key {
						return liveDiffPythonText{content: item.values[1]}, true
					}
				}
			}
			return liveDiffPythonText{}, false
		}
		value, ok := s.previewPythonText(ctx, node.ChildByFieldName("value"), state, arriving, depth+1)
		bounds := node.ChildByFieldName("subscript")
		if !ok || value.tip != 0 || value.arriving || bounds == nil {
			return liveDiffPythonText{}, false
		}
		runes := []rune(value.content)
		if bounds.Kind() != "slice" {
			index, known := s.pythonInt(ctx, bounds, state, depth+1)
			if index < 0 {
				index += len(runes)
			}
			if !known || index < 0 || index >= len(runes) {
				return liveDiffPythonText{}, false
			}
			return liveDiffPythonText{content: string(runes[index])}, true
		}
		start, stop, ok := s.pythonSlice(ctx, bounds, state, len(runes))
		return liveDiffPythonText{content: string(runes[start:stop])}, ok
	case "binary_operator":
		if s.text(node.ChildByFieldName("operator")) == "*" {
			return s.pythonRepeat(ctx, node, state, arriving, depth+1)
		}
		if s.text(node.ChildByFieldName("operator")) != "+" {
			return liveDiffPythonText{}, false
		}
		left, okLeft := s.previewPythonText(ctx, node.ChildByFieldName("left"), state, arriving, depth+1)
		right, okRight := s.previewPythonText(ctx, node.ChildByFieldName("right"), state, arriving, depth+1)
		if !okLeft || !okRight {
			return liveDiffPythonText{}, false
		}
		return liveDiffPythonConcat(left, right, liveDiffArriving(node.ChildByFieldName("right"), arriving))
	}
	replace, values := sourceCall(node)
	if replace == nil || replace.Kind() != "attribute" {
		return liveDiffPythonText{}, false
	}
	object := replace.ChildByFieldName("object")
	attribute := s.text(replace.ChildByFieldName("attribute"))
	if value, ok := s.pythonTextTransform(ctx, attribute, object, values, state, arriving, depth+1); ok {
		return value, true
	}
	if attribute == "read_text" || attribute == "read" {
		if positional, ok := liveDiffPythonArguments(s, values, "encoding", "errors", "newline"); !ok || len(positional) != 0 {
			return liveDiffPythonText{}, false
		}
		var path string
		var newline *string
		var reading *liveDiffPythonHandle
		switch {
		case attribute == "read_text":
			var valid bool
			if newline, valid = s.pythonNewline(values); !valid {
				return liveDiffPythonText{}, false
			}
			paths := s.paths(object)
			if len(paths) != 1 {
				return liveDiffPythonText{}, false
			}
			path = paths[0]
			if liveDiffPythonOpenConflict(state, path, false) {
				return liveDiffPythonText{}, false
			}
		case object != nil && object.Kind() == "identifier":
			handle, found := state.handles[s.text(object)]
			if !found || handle.write || handle.closed || len(values) != 0 {
				return liveDiffPythonText{}, false
			}
			path, newline = handle.path, handle.newline
			reading = handle
		default:
			handle, ok := s.pythonOpen(object)
			if !ok || handle.write || len(values) != 0 || liveDiffPythonOpenConflict(state, handle.path, false) {
				return liveDiffPythonText{}, false
			}
			path, newline = handle.path, handle.newline
		}
		if !filepath.IsAbs(path) {
			return liveDiffPythonText{}, false
		}
		key := [2]uint{node.StartByte(), node.EndByte()}
		if reading != nil {
			if value, cached := state.reads[key]; cached {
				return value, true
			}
			if reading.consumed {
				value := liveDiffPythonText{path: path}
				state.reads[key] = value
				return value, true
			}
		}
		content, exists := state.written[path]
		var err error
		if !exists {
			content, exists, err = liveDiffSourceRead(ctx, path, liveDiffPreviewFile)
		}
		if newline == nil {
			content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
		}
		value := liveDiffPythonText{path: path, content: content}
		if reading != nil && exists && err == nil {
			reading.consumed = true
			state.reads[key] = value
		}
		return value, exists && err == nil
	}
	if attribute != "replace" || len(values) < 2 || len(values) > 3 {
		return liveDiffPythonText{}, false
	}
	value, ok := s.previewPythonText(ctx, object, state, arriving, depth+1)
	if !ok {
		return liveDiffPythonText{}, false
	}
	oldValue, okOld := s.previewPythonText(ctx, values[0], state, arriving, depth+1)
	nextValue, okNext := s.previewPythonText(ctx, values[1], state, arriving, depth+1)
	if !okOld || !okNext || oldValue.tip != 0 || oldValue.arriving || liveDiffArriving(values[0], arriving) {
		return liveDiffPythonText{}, false
	}
	old, next := oldValue.content, nextValue.content
	count := -1
	if len(values) == 3 {
		if count, ok = s.pythonInt(ctx, values[2], state, depth+1); !ok {
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
	nextArriving := liveDiffArriving(values[1], arriving) || nextValue.arriving || nextValue.tip > 0
	if at := strings.Index(before, old); at >= 0 && nextArriving && count != 0 {
		value.tip = at + len(next)
		if nextValue.tip > 0 {
			value.tip = at + nextValue.tip
		}
	}
	return value, true
}
