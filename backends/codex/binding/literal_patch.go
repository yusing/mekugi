package binding

import (
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// Inspect the entire exec syntax tree rather than only transparent
// one-call wrappers. This recognizes literal patch arguments in ordinary JS
// sequencing and Promise batches, plus immutable top-level literal bindings
// used by a top-level expression. It never evaluates JavaScript or takes over
// the host tool. Dynamic arguments have no trustworthy pre-edit baseline.
// Source: apps/mekugi/internal/router/native_apply_patch.go:33:115@72549ff1.
func LiteralPatchInputs(source string) []string {
	if len(source) > commandanalysis.MaxScriptBytes {
		return nil
	}
	bytes := []byte(source)
	tree, err := sourceparse.Parse(bytes, sourceparse.JavaScript, nil)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil
	}
	bindings := make(map[string]string)
	for index := range root.NamedChildCount() {
		statement := root.NamedChild(uint(index))
		if statement.Kind() != "lexical_declaration" || statement.ChildCount() == 0 ||
			statement.Child(0).Kind() != "const" {
			continue
		}
		for childIndex := range statement.NamedChildCount() {
			declaration := statement.NamedChild(uint(childIndex))
			if declaration.Kind() != "variable_declarator" {
				continue
			}
			name, value := declaration.ChildByFieldName("name"), declaration.ChildByFieldName("value")
			if name == nil || name.Kind() != "identifier" {
				continue
			}
			literal, ok := sourceparse.JavaScriptValue(value, bytes)
			if text, isText := literal.(string); ok && isText {
				bindings[name.Utf8Text(bytes)] = text
			}
		}
	}
	var patches []string
	for rootIndex := range root.NamedChildCount() {
		rootStatement := root.NamedChild(uint(rootIndex))
		allowBinding := rootStatement.Kind() == "expression_statement" ||
			rootStatement.Kind() == "lexical_declaration" || rootStatement.Kind() == "variable_declaration"
		type visit struct {
			node         *sitter.Node
			allowBinding bool
		}
		stack := []visit{{node: rootStatement, allowBinding: allowBinding}}
		for len(stack) != 0 {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			node := current.node
			if node != rootStatement {
				switch node.Kind() {
				case "arrow_function", "function_expression", "function_declaration",
					"generator_function", "generator_function_declaration", "method_definition", "class":
					current.allowBinding = false
				}
			}
			if args, ok := sourceparse.JavaScriptCallArguments(node, bytes, "tools", "apply_patch"); ok && len(args) == 1 {
				if value, ok := sourceparse.JavaScriptValue(args[0], bytes); ok {
					if patch, ok := value.(string); ok {
						patches = append(patches, patch)
					}
				} else if current.allowBinding && args[0].Kind() == "identifier" {
					if patch, ok := bindings[args[0].Utf8Text(bytes)]; ok {
						patches = append(patches, patch)
					}
				}
			}
			if len(patches) > 32 {
				return nil
			}
			for index := int(node.NamedChildCount()) - 1; index >= 0; index-- {
				stack = append(stack, visit{node: node.NamedChild(uint(index)), allowBinding: current.allowBinding})
			}
		}
	}
	return patches
}
