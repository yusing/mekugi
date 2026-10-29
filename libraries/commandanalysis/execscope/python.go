package execscope

import (
	"strings"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type execPythonValues struct {
	paths, segments               []string
	unknownPaths, unknownSegments bool
}

// A lexical pass cannot use pre-loop values for names changed by that loop:
// subsequent iterations may observe the later assignment instead. Assignments
// visited in the body can still establish fresh candidates for that iteration.
func (s *execSourceScope) clearPythonLoopBindings(node *sitter.Node, depth int) {
	if node == nil {
		return
	}
	s.nodes++
	if depth > 256 || s.nodes > 50_000 {
		s.result.open = true
		return
	}
	if node.Kind() == "assignment" || node.Kind() == "augmented_assignment" || node.Kind() == "for_statement" || node.Kind() == "named_expression" {
		left := node.ChildByFieldName("left")
		if node.Kind() == "named_expression" {
			left = node.ChildByFieldName("name")
		}
		var targets []*sitter.Node
		if left != nil {
			targets = append(targets, left)
		}
		for len(targets) != 0 {
			left, targets = targets[0], targets[1:]
			if left.Kind() != "identifier" {
				if left.Kind() == "pattern_list" || left.Kind() == "tuple_pattern" || left.Kind() == "list_pattern" {
					for i := range left.NamedChildCount() {
						targets = append(targets, left.NamedChild(uint(i)))
					}
				}
				continue
			}
			name := s.text(left)
			delete(s.state.Paths, name)
			delete(s.state.Segments, name)
			delete(s.pythonItems, name)
		}
	}
	for i := range node.NamedChildCount() {
		s.clearPythonLoopBindings(node.NamedChild(uint(i)), depth+1)
	}
}

func (s *execSourceScope) appendPythonItem(items *[2]execPythonValues, key, value *sitter.Node) bool {
	for column, node := range []*sitter.Node{key, value} {
		paths, segments := s.paths(node), s.literalSegments(node, 0)
		item := &items[column]
		item.unknownPaths = item.unknownPaths || len(paths) == 0
		item.unknownSegments = item.unknownSegments || len(segments) == 0
		if len(item.paths)+len(paths) > ListingLimit || len(item.segments)+len(segments) > ListingLimit {
			return false
		}
		item.paths = append(item.paths, paths...)
		item.segments = append(item.segments, segments...)
	}
	return true
}

// Keep keys and values separate: an unknown value need not hide literal output
// names. These are bounded syntax-derived candidates, not evaluated Python.
func (s *execSourceScope) pythonDictionaryItems(node *sitter.Node) ([2]execPythonValues, bool) {
	var items [2]execPythonValues
	if node == nil || node.Kind() != "dictionary" {
		return items, false
	}
	for i := range node.NamedChildCount() {
		pair := node.NamedChild(uint(i))
		if pair.Kind() == "comment" {
			continue
		}
		if pair.Kind() != "pair" || i >= ListingLimit {
			return [2]execPythonValues{}, false
		}
		if !s.appendPythonItem(&items, pair.ChildByFieldName("key"), pair.ChildByFieldName("value")) {
			return [2]execPythonValues{}, false
		}
	}
	return items, true
}

func (s *execSourceScope) bindPythonItems(left, right *sitter.Node) {
	// Python evaluates the iterable before assigning loop targets, which may
	// themselves shadow the dictionary's name.
	function, args := sourceparse.Call(right)
	var items [2]execPythonValues
	ok := false
	if function != nil {
		object := function.ChildByFieldName("object")
		items, ok = s.pythonItems[s.text(object)]
		if !ok {
			items, ok = s.pythonDictionaryItems(object)
		}
	}
	s.clearPythonLoopBindings(left.Parent().ChildByFieldName("body"), 0)
	// Clear old bindings even when the new iterable cannot be resolved.
	for i := range left.NamedChildCount() {
		name := s.text(left.NamedChild(uint(i)))
		delete(s.state.Paths, name)
		delete(s.state.Segments, name)
		delete(s.texts, name)
		delete(s.pythonItems, name)
	}
	if function == nil || s.text(function.ChildByFieldName("attribute")) != "items" || len(args) != 0 || left.NamedChildCount() != 2 {
		return
	}
	for i := range left.NamedChildCount() {
		if left.NamedChild(uint(i)).Kind() != "identifier" {
			return
		}
	}
	if !ok {
		return
	}
	if s.state.Segments == nil {
		s.state.Segments = make(map[string][]string)
	}
	for column, values := range items {
		name := s.text(left.NamedChild(uint(column)))
		if values.unknownPaths {
			values.paths = nil
		}
		if values.unknownSegments {
			values.segments = nil
		}
		s.state.Paths[name], s.state.Segments[name] = values.paths, values.segments
	}
}

// Distinguish string replacement from Path.replace without evaluating content.
func (s *execSourceScope) pythonText(node *sitter.Node, depth int) bool {
	if node == nil || depth >= 32 {
		return false
	}
	if node.Kind() == "string" {
		return true
	}
	if node.Kind() == "identifier" {
		return s.texts[s.text(node)]
	}
	if node.Kind() == "parenthesized_expression" && node.NamedChildCount() == 1 {
		return s.pythonText(node.NamedChild(0), depth+1)
	}
	if node.Kind() == "subscript" {
		return s.pythonText(node.ChildByFieldName("value"), depth+1)
	}
	if node.Kind() == "binary_operator" && s.text(node.ChildByFieldName("operator")) == "+" {
		return s.pythonText(node.ChildByFieldName("left"), depth+1) && s.pythonText(node.ChildByFieldName("right"), depth+1)
	}
	function, _ := sourceparse.Call(node)
	if function == nil {
		return false
	}
	name := s.text(function.ChildByFieldName("attribute"))
	if name == "read_text" || name == "read_bytes" || name == "read" {
		return true
	}
	return name == "replace" && s.pythonText(function.ChildByFieldName("object"), depth+1)
}

func execPythonScope(input execProviderInput) execProviderResult {
	source, script, reason := execProgramSource(input)
	if reason != "" {
		return execProviderResult{open: true, reason: reason}
	}
	return inspectExecSource(input, source, script, sourceparse.Python, true)
}

func (s *execSourceScope) pythonCall(function *sitter.Node, base string, args []*sitter.Node) {
	arg := func(index int) *sitter.Node {
		if index < len(args) {
			return args[index]
		}
		return nil
	}
	object := function.ChildByFieldName("object")
	name := s.text(object)
	if base != "items" {
		// Mutating or unknown dictionary methods invalidate the snapshot.
		delete(s.pythonItems, name)
	}
	for _, argument := range args {
		if argument.Kind() == "keyword_argument" {
			argument = argument.ChildByFieldName("value")
		}
		delete(s.pythonItems, s.text(argument))
	}
	module := object == nil || name == "os" || name == "shutil"
	switch base {
	case "open":
		var positional []*sitter.Node
		var modeNode *sitter.Node
		for _, node := range args {
			if node.Kind() == "keyword_argument" {
				if s.text(node.ChildByFieldName("name")) == "mode" {
					modeNode = node.ChildByFieldName("value")
				}
			} else if node.Kind() != "comment" {
				positional = append(positional, node)
			}
		}
		modeIndex := 1
		if len(s.paths(object)) != 0 {
			modeIndex = 0
		}
		if modeNode == nil && modeIndex < len(positional) {
			modeNode = positional[modeIndex]
		}
		mode, ok := s.literal(modeNode)
		if ok && strings.ContainsAny(mode, "wax+") {
			if len(s.paths(object)) != 0 {
				s.add(object, false)
			} else {
				s.add(arg(0), false)
			}
		} else if modeNode != nil && !ok {
			s.result.open = true
		}
	case "write_text", "write_bytes", "touch", "mkdir":
		s.add(object, false)
	case "unlink", "rmdir":
		if module {
			s.add(arg(0), true)
		} else {
			s.add(object, true)
		}
	case "remove", "makedirs", "rmtree":
		s.add(arg(0), true)
	case "rename", "replace", "move":
		if base == "replace" && s.pythonText(object, 0) {
			return
		}
		if module {
			s.add(arg(0), true)
			s.add(arg(1), true)
		} else {
			s.add(object, true)
			s.add(arg(0), true)
		}
	case "copy", "copy2", "copyfile", "copytree":
		if module {
			s.add(arg(1), true)
		} else {
			s.add(arg(0), true)
		}
	case "copy_into", "move_into":
		s.add(arg(0), true)
		if base == "move_into" {
			s.add(object, true)
		}
	case "symlink_to", "hardlink_to":
		s.add(object, false)
	case "input":
		if s.text(function) == "fileinput.input" {
			for _, node := range args {
				if node.Kind() == "keyword_argument" && s.text(node.ChildByFieldName("name")) == "inplace" && s.text(node.ChildByFieldName("value")) == "True" {
					s.add(arg(0), false)
				}
			}
		}
	}
}
