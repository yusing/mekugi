package presentation

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// ToolGeneration describes input being authored, never an executed operation.
// It retains source only for JavaScript cells and literal identity refinement.
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
	label, _ := generationIdentity(g.name)
	if label != "" {
		if g.task != "" {
			return label + " `" + strings.ReplaceAll(g.task, "`", "") + "`"
		}
		return label
	}
	if g.javascript && g.name == "" {
		return "Emitting JavaScript"
	}
	return "Calling `" + strings.ReplaceAll(g.name, "`", "") + "`"
}

// generationIdentity owns display wording and the optional literal argument that
// refines it. Payloads (commands, messages, prompts) are not tool identities.
func generationIdentity(name string) (label, key string) {
	name = strings.TrimPrefix(strings.TrimPrefix(name, "functions."), "tools.")
	name = strings.ReplaceAll(name, ".", "__")
	switch name {
	case "request_user_input", "request_user_input_async":
		return "Asking user question", ""
	case "mcp__threadcontrol__spawn_agent":
		return "Spawning subagent", "task_name"
	case "mcp__threadcontrol__followup_task":
		return "Assigning follow-up to", "target"
	case "mcp__threadcontrol__send_message":
		return "Sending message to", "target"
	case "mcp__threadcontrol__interrupt_agent":
		return "Interrupting agent", "target"
	case "mcp__threadcontrol__inspect":
		return "Inspecting agent", "target"
	case "mcp__threadcontrol__list_agents":
		return "Listing agents", "path_prefix"
	case "mcp__threadcontrol__wait_agent":
		return "Waiting for agents", ""
	case "exec":
		return "Emitting JavaScript", ""
	case "wait":
		return "Waiting for JavaScript", "cell_id"
	case "apply_patch":
		return "Emitting a patch", ""
	case "exec_command":
		return "Executing a command", ""
	case "write_stdin":
		return "Interacting with a command", ""
	case "view_image":
		return "Viewing an image", "path"
	case "list_mcp_resources":
		return "Listing resources", "server"
	case "list_mcp_resource_templates":
		return "Listing resource templates", "server"
	case "read_mcp_resource":
		return "Reading resource", "uri"
	case "get_context_remaining":
		return "Checking remaining context", ""
	case "clock__curr_time", "curr_time":
		return "Checking time", ""
	case "clock__sleep", "sleep":
		return "Sleeping", ""
	case "web__run":
		return "Browsing the web", ""
	case "image_gen__imagegen":
		return "Generating an image", ""
	}
	return "", ""
}

// Append reads incomplete syntax without evaluating it. Quoted payload chunks
// cannot identify another tool, so they do not repeatedly reparse a large body.
func (g *ToolGeneration) Append(delta string) {
	if !g.javascript && g.input.Len() == 0 {
		if _, key := generationIdentity(g.name); key == "" {
			return
		}
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
	var identityKey string
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
				_, identityKey = generationIdentity(name)
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
		if parent := node.Parent(); identityKey != "" && parent != nil && node.StartByte() >= calleeEnd && parent.StartByte() == argumentParent && parent.Kind() == argumentKind {
			switch node.Kind() {
			case "spread_element", "method_definition", "shorthand_property_identifier":
				// These members can replace an earlier literal identity. Do not
				// evaluate them or keep a name that may no longer be supplied.
				g.task = ""
			case "pair":
				key, value := node.ChildByFieldName("key"), node.ChildByFieldName("value")
				if key != nil && value != nil {
					name := key.Utf8Text(source)
					if key.Kind() == "computed_property_name" {
						g.task = ""
					} else {
						if key.Kind() == "string" {
							name, _ = sourceparse.JavaScriptString(name)
						}
						if name == identityKey {
							g.task, _ = sourceparse.JavaScriptString(value.Utf8Text(source))
						}
					}
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
