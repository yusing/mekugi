package router

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

var execPythonLanguage = sitter.NewLanguage(python.Language())

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
	function, _ := sourceCall(node)
	if function == nil {
		return false
	}
	name := s.text(function.ChildByFieldName("attribute"))
	if name == "read_text" {
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
	module := object == nil || name == "os" || name == "shutil"
	switch base {
	case "open":
		modeNode := arg(1)
		if len(s.paths(object)) != 0 {
			modeNode = arg(0)
		}
		for _, node := range args {
			if node.Kind() == "keyword_argument" && s.text(node.ChildByFieldName("name")) == "mode" {
				modeNode = node.ChildByFieldName("value")
			}
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
