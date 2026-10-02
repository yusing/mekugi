package router

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

var execPythonLanguage = sitter.NewLanguage(python.Language())

type execPythonValues struct {
	paths, segments []string
}

// Keep keys and values separate: an unknown value need not hide literal output
// names. These are bounded syntax-derived candidates, not evaluated Python.
func (s *execSourceScope) pythonDictionaryItems(node *sitter.Node) ([2]execPythonValues, bool) {
	var items [2]execPythonValues
	if node == nil || node.Kind() != "dictionary" {
		return items, false
	}
	unknownPaths, unknownSegments := [2]bool{}, [2]bool{}
	for i := range node.NamedChildCount() {
		pair := node.NamedChild(uint(i))
		if pair.Kind() == "comment" {
			continue
		}
		if pair.Kind() != "pair" || i >= maxExecListingEntries {
			return [2]execPythonValues{}, false
		}
		for column, field := range []string{"key", "value"} {
			value := pair.ChildByFieldName(field)
			paths, segments := s.paths(value), s.literalSegments(value, 0)
			unknownPaths[column] = unknownPaths[column] || len(paths) == 0
			unknownSegments[column] = unknownSegments[column] || len(segments) == 0
			if len(items[column].paths)+len(paths) > maxExecListingEntries || len(items[column].segments)+len(segments) > maxExecListingEntries {
				return [2]execPythonValues{}, false
			}
			items[column].paths = append(items[column].paths, paths...)
			items[column].segments = append(items[column].segments, segments...)
		}
	}
	for column := range items {
		if unknownPaths[column] {
			items[column].paths = nil
		}
		if unknownSegments[column] {
			items[column].segments = nil
		}
	}
	return items, true
}

func (s *execSourceScope) bindPythonItems(left, right *sitter.Node) {
	// Python evaluates the iterable before assigning loop targets, which may
	// themselves shadow the dictionary's name.
	function, args := sourceCall(right)
	var items [2]execPythonValues
	ok := false
	if function != nil {
		object := function.ChildByFieldName("object")
		items, ok = s.pythonItems[s.text(object)]
		if !ok {
			items, ok = s.pythonDictionaryItems(object)
		}
	}
	// Clear old bindings even when the new iterable cannot be resolved.
	for i := range left.NamedChildCount() {
		name := s.text(left.NamedChild(uint(i)))
		delete(s.vars, name)
		delete(s.segments, name)
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
	if s.segments == nil {
		s.segments = make(map[string][]string)
	}
	for column, values := range items {
		name := s.text(left.NamedChild(uint(column)))
		s.vars[name], s.segments[name] = values.paths, values.segments
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
	function, _ := sourceCall(node)
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
	return inspectExecSource(input, source, script, execPythonLanguage, true)
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
