package presentation

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// ToolGeneration describes input being authored, never an executed operation.
// It retains source only for JavaScript cells and spawn-name refinement.
type ToolGeneration struct {
	name, task     string
	javascript     bool
	input          strings.Builder
	quote          byte
	escaped        bool
	templateDollar bool
}

func NewToolGeneration(name string, javascript bool) *ToolGeneration {
	return &ToolGeneration{name: name, javascript: javascript}
}

func (g *ToolGeneration) Label() string {
	name := strings.TrimPrefix(strings.TrimPrefix(g.name, "functions."), "tools.")
	name = strings.ReplaceAll(name, ".", "__")
	switch name {
	case "request_user_input", "request_user_input_async", "AskUserQuestion":
		return "Asking user question"
	case "mcp__threadcontrol__spawn_agent":
		if g.task != "" {
			return "Spawning subagent `" + strings.ReplaceAll(g.task, "`", "") + "`"
		}
		return "Spawning subagent"
	case "apply_patch":
		return "Emitting a patch"
	case "exec_command", "Bash":
		return "Executing a command"
	case "write_stdin":
		return "Interacting with a command"
	case "view_image":
		return "Viewing an image"
	}
	if g.javascript && g.name == "" {
		return "Emitting JavaScript"
	}
	return "Calling `" + strings.ReplaceAll(g.name, "`", "") + "`"
}

// Append reads incomplete syntax without evaluating it. Quoted payload chunks
// cannot identify another tool, so they do not repeatedly reparse a large body.
func (g *ToolGeneration) Append(delta string) {
	if !g.javascript && !strings.HasSuffix(g.name, "spawn_agent") {
		return
	}
	g.input.WriteString(delta)
	changed := false
	for i := range len(delta) {
		c := delta[i]
		if g.escaped {
			g.escaped = false
			g.templateDollar = false
			continue
		}
		if g.quote != 0 {
			if g.quote == '`' {
				if g.templateDollar && c == '{' {
					g.quote, g.templateDollar, changed = 0, false, true
					continue
				}
				g.templateDollar = c == '$'
			}
			if c == '\\' {
				g.escaped = true
				g.templateDollar = false
			} else if c == g.quote {
				g.quote, changed = 0, true
			}
			continue
		}
		changed = true
		if c == '\'' || c == '"' || c == '`' {
			g.quote = c
		}
	}
	if !changed {
		return
	}
	input := g.input.String()
	if !g.javascript {
		input = "tools." + g.name + "(" + input
	}
	source := []byte(input)
	tree, err := sourceparse.Parse(source, sourceparse.JavaScript, nil)
	if err != nil {
		return
	}
	defer tree.Close()
	// Only syntax-proven string delimiters can suppress later scans. Quotes in
	// comments and regular expressions must not hide a subsequent real callee.
	g.quote = 0
	var calleeEnd uint
	var argumentParent uint
	var argumentKind string
	var visit func(*sitter.Node)
	visit = func(node *sitter.Node) {
		if node.Kind() == "${" {
			g.quote = 0
		} else if node.Kind() == "}" && node.Parent() != nil && node.Parent().Kind() == "template_substitution" {
			g.quote = '`'
		}
		if kind := node.Kind(); kind == "\"" || kind == "'" || kind == "`" {
			if g.quote == kind[0] {
				g.quote = 0
			} else if g.quote == 0 {
				g.quote = kind[0]
			}
		}
		if node.Kind() == "member_expression" {
			if name := generationToolName(node, source); name != "" && generationCall(node) {
				g.name, g.task, calleeEnd = name, "", node.EndByte()
				argumentKind = ""
				if parent := node.Parent(); parent != nil {
					if parent.Kind() == "ERROR" {
						argumentParent, argumentKind = parent.StartByte(), parent.Kind()
						for i := range parent.NamedChildCount() {
							if object := parent.NamedChild(uint(i)); object.Kind() == "object" && object.StartByte() > calleeEnd {
								argumentParent, argumentKind = object.StartByte(), object.Kind()
								break
							}
						}
					} else if args := parent.ChildByFieldName("arguments"); args != nil {
						for i := range args.NamedChildCount() {
							if object := args.NamedChild(uint(i)); object.Kind() == "object" {
								argumentParent, argumentKind = object.StartByte(), object.Kind()
								break
							}
						}
					}
				}
			}
		}
		if node.Kind() == "pair" && node.StartByte() >= calleeEnd && strings.HasSuffix(g.name, "spawn_agent") {
			key, value := node.ChildByFieldName("key"), node.ChildByFieldName("value")
			parent := node.Parent()
			if key != nil && value != nil && parent != nil && parent.StartByte() == argumentParent && parent.Kind() == argumentKind {
				name := key.Utf8Text(source)
				if key.Kind() == "string" {
					name, _ = sourceparse.JavaScriptString(name)
				}
				if name == "task_name" {
					g.task, _ = sourceparse.JavaScriptString(value.Utf8Text(source))
				}
			}
		}
		for i := range node.ChildCount() {
			visit(node.Child(uint(i)))
		}
	}
	visit(tree.RootNode())
	if g.quote != '`' {
		g.templateDollar = false
	}
	g.escaped = false
	if g.quote != 0 {
		for i := len(input) - 1; i >= 0 && input[i] == '\\'; i-- {
			g.escaped = !g.escaped
		}
	}
}

// Incomplete calls can live directly under ERROR until their argument closes.
// Use syntax tokens so intervening comments are trivia, not lost identity.
func generationCall(node *sitter.Node) bool {
	parent := node.Parent()
	if parent == nil {
		return false
	}
	if function := parent.ChildByFieldName("function"); function != nil && function.StartByte() == node.StartByte() && function.EndByte() == node.EndByte() {
		return true
	}
	if parent.Kind() != "ERROR" {
		return false
	}
	for i := range parent.ChildCount() {
		child := parent.Child(uint(i))
		if child.StartByte() >= node.EndByte() && child.Kind() != "comment" {
			return child.Kind() == "("
		}
	}
	return false
}

func generationToolName(node *sitter.Node, source []byte) string {
	if node == nil || node.Kind() != "member_expression" || node.ChildByFieldName("optional_chain") != nil {
		return ""
	}
	object, property := node.ChildByFieldName("object"), node.ChildByFieldName("property")
	if object == nil || property == nil || property.Kind() != "property_identifier" {
		return ""
	}
	if object.Kind() == "identifier" && object.Utf8Text(source) == "tools" {
		return property.Utf8Text(source)
	}
	if prefix := generationToolName(object, source); prefix != "" {
		return prefix + "." + property.Utf8Text(source)
	}
	return ""
}
