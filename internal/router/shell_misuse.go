package router

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	treeSitterTypeScript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

const shellTypeScriptDiagnostic = "shell: [shell-typescript-misuse] Rejected before execution: the Bash body is invalid Bash but valid TypeScript/JavaScript. Use functions.exec for Code Mode helpers such as tools, ALL_TOOLS, and text when available; call collaboration tools directly. For an ordinary script, select an explicit interpreter with a shebang such as #!node or #!bun. No script or command template was executed."

const shellCodeModeRecoveryWarning = "shell: [shell-code-mode-recovered] Recovered Code Mode JavaScript submitted through functions.shell. Submit shell commands directly to functions.shell, without tools.exec_command or Promise wrappers. Use functions.exec only for other Code Mode helpers."

const execShellRecoveryWarning = "exec: [exec-shell-recovered] Recovered an interpreter script submitted through functions.exec. Use functions.shell for shell directives and interpreter scripts."

// A valid JavaScript program keeps Code Mode semantics, including its hashbang.
// Only a parseable shell header or batch opts invalid JavaScript into shell
// translation. Bare commands and malformed headers are never guessed.
func execShellRecovery(input string) bool {
	if !strings.HasPrefix(input, "#!") && !shellsyntax.IsBatch(input) {
		return false
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if parser.SetLanguage(codeModeJavaScriptLanguage) != nil {
		return false
	}
	tree := parser.Parse([]byte(input), nil)
	if tree == nil {
		return false
	}
	defer tree.Close()
	if !tree.RootNode().HasError() {
		return false
	}
	_, err := shellsyntax.Split(input)
	return err == nil
}

// Recovery requires JavaScript syntax and a reference to the Code Mode runtime,
// not text that merely resembles a call. Explicit shell headers, directives,
// retained references, and valid Bash never opt into it.
func shellCodeModeRecovery(contribution toolContribution, input string) bool {
	if contribution.PluginID != builtinToolsPluginID || contribution.Name != "shell" {
		return false
	}
	program := strings.TrimLeft(input, " \t\r\n")
	if strings.HasPrefix(program, "#!") || shellsyntax.IsBatch(program) {
		return false
	}
	if _, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(program), ""); err == nil {
		return false
	}
	return inspectCodeModeRuntime(program).runtime
}

type codeModeRuntimeUsage struct {
	runtime              bool
	execCommand          bool
	warningOffset        int
	nativeWarningOffset  int
	nativeWarningPresent bool
	textShadowed         bool
}

func inspectCodeModeRuntime(program string) codeModeRuntimeUsage {
	var usage codeModeRuntimeUsage
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(codeModeJavaScriptLanguage); err != nil {
		return usage
	}
	source := []byte(program)
	tree := parser.Parse(source, nil)
	if tree == nil {
		return usage
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		return usage
	}
	// Do not displace a leading pragma, comment, or JavaScript directive prologue.
	for i := range root.NamedChildCount() {
		node := root.NamedChild(uint(i))
		if node.Kind() == "comment" || node.Kind() == "hash_bang_line" {
			continue
		}
		if node.Kind() == "expression_statement" && node.NamedChildCount() == 1 && node.NamedChild(0).Kind() == "string" {
			continue
		}
		usage.warningOffset = int(node.StartByte())
		break
	}
	usage.nativeWarningOffset = usage.warningOffset
	canonical := strings.HasPrefix(program[usage.warningOffset:], codeModeExecCallPrefix)
	for i := range root.NamedChildCount() {
		node := root.NamedChild(uint(i))
		if node.Kind() != "expression_statement" {
			continue
		}
		text := node.Utf8Text(source)
		if text == strings.TrimSpace(misuseWarningProjection(nativeExecCommandWarning)) {
			usage.nativeWarningPresent = true
		}
		if canonical && (text == codeModeOutputProjection || text == codeModeJSONProjection || strings.HasPrefix(text, codeModeMetadataProjection)) {
			usage.nativeWarningOffset = int(node.StartByte())
			canonical = false
		}
	}

	// Conservatively exclude a runtime name if any binding or assignment in the
	// program owns it. This avoids inventing runtime references for local helpers
	// without trying to emulate JavaScript's lexical-scope or execution semantics.
	shadowed := make(map[string]bool)
	var bind func(*sitter.Node)
	bind = func(node *sitter.Node) {
		if node == nil {
			return
		}
		switch node.Kind() {
		case "identifier", "shorthand_property_identifier_pattern":
			switch name := node.Utf8Text(source); name {
			case "tools", "ALL_TOOLS", "text", "image", "audio", "generatedImage":
				shadowed[name] = true
			}
		}
		for i := range node.NamedChildCount() {
			bind(node.NamedChild(uint(i)))
		}
	}
	var bindings func(*sitter.Node)
	bindings = func(node *sitter.Node) {
		switch node.Kind() {
		case "variable_declarator", "function_declaration", "function_expression", "generator_function_declaration", "generator_function", "class_declaration", "class":
			bind(node.ChildByFieldName("name"))
		case "formal_parameters", "import_clause":
			bind(node)
		case "arrow_function", "catch_clause":
			bind(node.ChildByFieldName("parameter"))
		case "assignment_expression", "augmented_assignment_expression", "for_in_statement":
			left := node.ChildByFieldName("left")
			if left != nil && left.Kind() != "member_expression" && left.Kind() != "subscript_expression" {
				bind(left)
			}
		}
		for i := range node.NamedChildCount() {
			bindings(node.NamedChild(uint(i)))
		}
	}
	bindings(root)
	usage.textShadowed = shadowed["text"]
	var usesRuntime func(*sitter.Node)
	usesRuntime = func(node *sitter.Node) {
		if node.Kind() == "identifier" && node.Utf8Text(source) == "ALL_TOOLS" && !shadowed["ALL_TOOLS"] {
			usage.runtime = true
		}
		if node.Kind() == "call_expression" {
			callee := node.ChildByFieldName("function")
			if callee != nil && callee.Kind() == "identifier" && !shadowed[callee.Utf8Text(source)] {
				switch callee.Utf8Text(source) {
				case "text", "image", "audio", "generatedImage":
					usage.runtime = true
				}
			}
			if callee != nil && (callee.Kind() == "member_expression" || callee.Kind() == "subscript_expression") {
				object := callee.ChildByFieldName("object")
				if object != nil && object.Kind() == "identifier" && object.Utf8Text(source) == "tools" && !shadowed["tools"] {
					usage.runtime = true
					property := callee.ChildByFieldName("property")
					if property != nil && property.Utf8Text(source) == "exec_command" {
						usage.execCommand = true
					}
					index := callee.ChildByFieldName("index")
					if index != nil && index.Kind() == "string" {
						// Only literal bracket access, not dynamic expressions.
						value := index.Utf8Text(source)
						if value == `"exec_command"` || value == `'exec_command'` {
							usage.execCommand = true
						}
					}
				}
			}
		}
		for i := range node.NamedChildCount() {
			usesRuntime(node.NamedChild(uint(i)))
		}
	}
	usesRuntime(root)
	return usage
}

var shellTypeScriptLanguage = sitter.NewLanguage(treeSitterTypeScript.LanguageTypescript())

// Inspect the translator's normalized argv, so shebangs, directives, and retained
// scripts use the same interpreter and body as execution. Valid Bash wins even
// when it also parses as TypeScript; never reinterpret or execute rejected input.
func shellTypeScriptMisuse(contribution toolContribution, arguments []string) bool {
	if contribution.PluginID != builtinToolsPluginID || contribution.Name != "shell" ||
		len(arguments) < 2 || shellInterpreterName(arguments[0]) != "bash" {
		return false
	}
	parsed, err := shellsyntax.Parse(arguments[len(arguments)-1])
	if err != nil {
		return false
	}
	body := parsed.Body
	if _, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(body), ""); err == nil {
		return false
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(shellTypeScriptLanguage); err != nil {
		return false
	}
	tree := parser.Parse([]byte(body), nil)
	if tree == nil {
		return false
	}
	defer tree.Close()
	root := tree.RootNode()
	return root != nil && !root.HasError()
}
