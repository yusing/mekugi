package execscope

import (
	"strconv"
	"time"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// Only unmodified imported modules and the unshadowed import builtin establish
// argv values. This lexical proof is deliberately conservative across scopes
// and loops: a later mutation can affect an earlier expression on another pass.
func (s *execSourceScope) inspectPythonArgv(root *sitter.Node) {
	s.pythonSys = make(map[string]bool)
	s.pythonShadow = make(map[string]bool)
	s.pythonArgvSafe = true
	var visit func(*sitter.Node, int)
	var bind func(*sitter.Node, int)
	bind = func(node *sitter.Node, depth int) {
		if node == nil {
			return
		}
		s.nodes++
		if depth > 256 || s.nodes > 50_000 {
			s.pythonArgvSafe = false
			return
		}
		if node.Kind() == "identifier" {
			s.pythonShadow[s.text(node)] = true
			return
		}
		for i := range node.NamedChildCount() {
			bind(node.NamedChild(uint(i)), depth+1)
		}
	}
	visit = func(node *sitter.Node, depth int) {
		s.nodes++
		if depth > 256 || s.nodes > 50_000 || time.Now().After(s.input.deadline) {
			s.pythonArgvSafe = false
			return
		}
		switch node.Kind() {
		case "import_statement":
			for i := range node.NamedChildCount() {
				child := node.NamedChild(uint(i))
				name, alias := s.text(child), s.text(child)
				if child.Kind() == "aliased_import" {
					name, alias = s.text(child.ChildByFieldName("name")), s.text(child.ChildByFieldName("alias"))
				}
				if name == "sys" {
					s.pythonSys[alias] = true
					if alias == "__import__" {
						s.pythonShadow[alias] = true
					}
				} else {
					s.pythonShadow[alias] = true
				}
			}
		case "import_from_statement":
			bind(node, 0)
		case "wildcard_import":
			s.pythonArgvSafe = false
		case "assignment", "augmented_assignment", "for_statement":
			bind(node.ChildByFieldName("left"), 0)
		case "named_expression":
			bind(node.ChildByFieldName("name"), 0)
		case "parameters", "lambda_parameters", "delete_statement", "as_pattern_target":
			bind(node, 0)
		case "function_definition", "class_definition":
			bind(node.ChildByFieldName("name"), 0)
		case "attribute":
			if s.text(node.ChildByFieldName("attribute")) == "argv" {
				s.pythonArgvSeen = true
				parent := node.Parent()
				if parent == nil || parent.Kind() != "subscript" || parent.ChildByFieldName("value").Id() != node.Id() {
					s.pythonArgvSafe = false // An escaped list or method can mutate argv.
				} else if ancestor := parent.Parent(); ancestor != nil && (ancestor.Kind() == "assignment" || ancestor.Kind() == "augmented_assignment") && ancestor.ChildByFieldName("left").Id() == parent.Id() {
					s.pythonArgvSafe = false
				}
			}
		case "identifier":
			parent := node.Parent()
			if s.text(node) == "__import__" && parent != nil && parent.Kind() == "call" && parent.ChildByFieldName("function").Id() == node.Id() {
				break
			}
			if parent != nil && parent.Kind() != "attribute" && parent.Kind() != "dotted_name" && parent.Kind() != "aliased_import" {
				// Passing an imported module to unknown code loses its identity.
				s.pythonShadow[s.text(node)] = true
			}
			if parent != nil && parent.Kind() == "attribute" && parent.ChildByFieldName("object").Id() == node.Id() && s.text(parent.ChildByFieldName("attribute")) != "argv" {
				// Other module members can expose mutable state, including
				// __dict__ and modules. Their effects are not interpreted.
				s.pythonShadow[s.text(node)] = true
			}
		case "call":
			function, args := sourceparse.Call(node)
			if s.text(function) == "__import__" && len(args) == 1 {
				if name, ok := s.literal(args[0]); ok && name == "sys" {
					parent := node.Parent()
					if parent == nil || parent.Kind() != "attribute" || s.text(parent.ChildByFieldName("attribute")) != "argv" {
						s.pythonArgvSafe = false
					}
				}
			}
		}
		for i := range node.NamedChildCount() {
			visit(node.NamedChild(uint(i)), depth+1)
		}
	}
	visit(root, 0)
}

func (s *execSourceScope) pythonArgument(node *sitter.Node) (string, bool) {
	if !s.pythonArgvSafe || node == nil || node.Kind() != "subscript" {
		return "", false
	}
	value := node.ChildByFieldName("value")
	if value == nil || value.Kind() != "attribute" || s.text(value.ChildByFieldName("attribute")) != "argv" {
		return "", false
	}
	object := value.ChildByFieldName("object")
	module := object != nil && object.Kind() == "identifier" && s.pythonSys[s.text(object)] && !s.pythonShadow[s.text(object)]
	function, args := sourceparse.Call(object)
	if !module && s.text(function) == "__import__" && !s.pythonShadow["__import__"] && len(args) == 1 {
		name, literal := s.literal(args[0])
		module = literal && name == "sys"
	}
	if !module {
		return "", false
	}
	index := node.ChildByFieldName("subscript")
	if index == nil || index.Kind() != "integer" {
		return "", false
	}
	i, err := strconv.Atoi(s.text(index))
	if err != nil || i < 0 || i >= len(s.input.pythonArgv) {
		return "", false
	}
	return s.input.pythonArgv[i], true
}
