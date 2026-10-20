package commandanalysis

import (
	"cmp"
	"encoding/json"
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// MaxScriptBytes bounds static observation of a stock JavaScript cell.
const MaxScriptBytes = 1 << 20

// Source: internal/router/exec_observation.go:57:61,208:235,1656:1794@23344db6.
// Command is one stock command as the host will run it.
type Command struct {
	Command string
	Workdir string
	Shell   string `json:",omitempty"`
}

// ExecCommandArguments decodes stock exec_command arguments and resolves the
// working directory against the selected metadata directory. A command without
// its own shell runs in the session shell. A command for another environment
// is excluded from local observation.
func ExecCommandArguments(arguments, directory, sessionShell string) (Command, bool) {
	var args struct {
		Cmd           string          `json:"cmd"`
		Workdir       string          `json:"workdir"`
		Shell         string          `json:"shell"`
		EnvironmentID json.RawMessage `json:"environment_id"`
	}
	if json.Unmarshal([]byte(arguments), &args) != nil || !LocalEnvironment(args.EnvironmentID) {
		return Command{}, false
	}
	return resolveCommand(Command{Command: args.Cmd, Workdir: args.Workdir, Shell: args.Shell}, directory, sessionShell)
}

func resolveCommand(command Command, directory, sessionShell string) (Command, bool) {
	if command.Command == "" {
		return Command{}, false
	}
	if !filepath.IsAbs(command.Workdir) {
		if !filepath.IsAbs(directory) {
			return Command{}, false
		}
		command.Workdir = filepath.Join(directory, command.Workdir)
	}
	command.Workdir = filepath.Clean(command.Workdir)
	command.Shell = cmp.Or(command.Shell, sessionShell)
	return command, true
}

func LocalEnvironment(environmentID []byte) bool {
	return len(environmentID) == 0 || string(environmentID) == "null"
}

// LiteralExecCommands extracts literal tools.exec_command calls from a
// exec cell. Any other reference to the command tools, including
// write_stdin input that can drive a started process, makes the cell dynamic.
func LiteralExecCommands(source, directory, sessionShell string) (commands []Command, dynamic bool) {
	if len(source) > MaxScriptBytes || !strings.Contains(source, "tools") {
		return nil, false
	}
	bytes := []byte(source)
	tree, err := sourceparse.Parse(bytes, sourceparse.JavaScript, nil)
	if err != nil || tree == nil {
		return nil, true
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil, strings.Contains(source, "exec_command") || strings.Contains(source, "write_stdin")
	}
	handled := make(map[uintptr]bool)
	stack := []*sitter.Node{root}
	for len(stack) != 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if args, ok := sourceparse.JavaScriptCallArguments(node, bytes, "tools", "exec_command"); ok {
			function := node.ChildByFieldName("function")
			handled[function.Id()] = true
			command, literal := execLiteralCommand(args, bytes, directory, sessionShell)
			if !literal {
				dynamic = true
			} else {
				commands = append(commands, command)
			}
			if len(commands) > 32 {
				return nil, true
			}
		}
		if args, ok := sourceparse.JavaScriptCallArguments(node, bytes, "tools", "write_stdin"); ok && execPollOnly(args, bytes) {
			handled[node.ChildByFieldName("function").Id()] = true
		}
		switch node.Kind() {
		case "member_expression":
			if !handled[node.Id()] && (sourceparse.JavaScriptMemberPath(node, bytes, "tools", "exec_command") ||
				sourceparse.JavaScriptMemberPath(node, bytes, "tools", "write_stdin")) {
				dynamic = true
			}
		case "subscript_expression":
			if object := node.ChildByFieldName("object"); object != nil && object.Kind() == "identifier" && object.Utf8Text(bytes) == "tools" {
				dynamic = true
			}
		case "optional_chain":
			// tools?.exec_command is a call the literal matcher does not read.
			if parent := node.Parent(); parent != nil && parent.Kind() == "member_expression" {
				if object := parent.ChildByFieldName("object"); object != nil && object.Utf8Text(bytes) == "tools" {
					dynamic = true
				}
			}
		case "identifier", "shorthand_property_identifier", "shorthand_property_identifier_pattern":
			// A bare tools value can be aliased or passed away, after which its
			// command calls are not literal. Global objects and evaluated code
			// reach tools without naming it.
			switch node.Utf8Text(bytes) {
			case "tools":
				parent := node.Parent()
				if parent == nil || parent.Kind() != "member_expression" || parent.ChildByFieldName("object").Id() != node.Id() {
					dynamic = true
				}
			case "globalThis", "self", "window", "global", "eval", "Function":
				dynamic = true
			}
		case "this":
			dynamic = true
		}
		for index := int(node.NamedChildCount()) - 1; index >= 0; index-- {
			stack = append(stack, node.NamedChild(uint(index)))
		}
	}
	return commands, dynamic
}

// Poll metadata may depend on a prior result; only input bytes determine whether
// this call can drive a writer. Spreads, prototypes and computed keys stay open.
func execPollOnly(args []*sitter.Node, source []byte) bool {
	if len(args) != 1 || args[0].Kind() != "object" {
		return false
	}
	seen := make(map[string]bool)
	for i := range args[0].NamedChildCount() {
		pair := args[0].NamedChild(uint(i))
		key := ""
		if pair.Kind() == "shorthand_property_identifier" {
			key = pair.Utf8Text(source)
		} else if pair.Kind() == "pair" {
			node := pair.ChildByFieldName("key")
			switch node.Kind() {
			case "property_identifier":
				key = node.Utf8Text(source)
			case "string":
				key, _ = sourceparse.JavaScriptString(node.Utf8Text(source))
			}
		}
		if key == "" || key == "__proto__" || strings.ContainsRune(key, '\\') || seen[key] {
			return false
		}
		seen[key] = true
		if key == "chars" {
			value, ok := sourceparse.JavaScriptValue(pair.ChildByFieldName("value"), source)
			if !ok || value != "" {
				return false
			}
		}
	}
	return true
}

func execLiteralCommand(args []*sitter.Node, source []byte, directory, sessionShell string) (Command, bool) {
	if len(args) != 1 {
		return Command{}, false
	}
	value, ok := sourceparse.JavaScriptValue(args[0], source)
	object, isObject := value.(map[string]any)
	if !ok || !isObject {
		return Command{}, false
	}
	if _, remote := object["environment_id"]; remote {
		return Command{}, false
	}
	var command Command
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"cmd", &command.Command}, {"workdir", &command.Workdir}, {"shell", &command.Shell},
	} {
		value, present := object[field.name]
		if !present {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return Command{}, false
		}
		*field.target = text
	}
	return resolveCommand(command, directory, sessionShell)
}
