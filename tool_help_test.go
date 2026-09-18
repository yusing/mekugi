package mekugi

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestMekugi2ToolGrammarQuotedOperands(t *testing.T) {
	target := grammarTerminalRegexp(t, "TARGET_QUOTED")
	for _, value := range []string{
		`"text"`,
		`"quote\"slash\\solidus\/"`,
		`"line\ntext"`,
		`"line\u000Atext"`,
		`"line\u000atext"`,
		`"tab\ttext"`,
		`"tab\u0009text"`,
		"\"literal\ttext\"",
	} {
		if !target.MatchString(value) {
			t.Errorf("TARGET_QUOTED rejects valid value %q", value)
		}
	}
	for control := range 0x20 {
		encoded := fmt.Sprintf(`"before\u%04Xafter"`, control)
		if got, want := target.MatchString(encoded), control == '\t' || control == '\n'; got != want {
			t.Errorf("TARGET_QUOTED matches encoded U+%04X = %v, want %v", control, got, want)
		}
	}
	for _, value := range []string{
		`""`,
		"\"raw\nnewline\"",
		"\"raw\rreturn\"",
		"\"raw\x01control\"",
		`"return\r"`,
		`"return\u000D"`,
		`"vertical\u000btab"`,
	} {
		if target.MatchString(value) {
			t.Errorf("TARGET_QUOTED accepts invalid value %q", value)
		}
	}

	value := grammarTerminalRegexp(t, "QUOTED")
	for _, input := range []string{`""`, `"line\nvalue"`, `"return\rvalue"`, `"tab\tvalue"`} {
		if !value.MatchString(input) {
			t.Errorf("QUOTED rejects valid value %q", input)
		}
	}
}

func TestToolGrammarHeredocs(t *testing.T) {
	for _, rule := range []string{
		`heredoc_mutation: "type" SP target SP heredoc`,
		`| "add" SP add_destination SP heredoc`,
		`heredoc_initializer: "type" SP heredoc`,
		`shell_command: "shell" SP (heredoc | SHELL_INLINE)`,
		`heredoc: HEREDOC_MARKER NL HEREDOC_BODY_LINE* HEREDOC_END`,
	} {
		if !strings.Contains(toolGrammar, rule) {
			t.Errorf("missing rule %q", rule)
		}
	}
	marker := grammarTerminalRegexp(t, "HEREDOC_MARKER")
	for _, input := range []string{"<<END", "<<'END'", `<<"END"`, "<<- END", "<<'END HERE'", "<<PATCH-", "<<TEXT", "<<E'N'D", `<<\END`} {
		if !marker.MatchString(input) {
			t.Errorf("delimiter rejected: %q", input)
		}
	}
	for _, input := range []string{"<<", "<<END extra", "<<'END", "<<END\n"} {
		if marker.MatchString(input) {
			t.Errorf("invalid delimiter accepted: %q", input)
		}
	}
	body := grammarTerminalRegexp(t, "HEREDOC_BODY_LINE")
	for _, input := range []string{"\n", "|\n", "type <<END\n", "PATCH\r\n", "TEXT\n", "$HOME\n", "one\rtwo\n"} {
		if !body.MatchString(input) {
			t.Errorf("literal body rejected: %q", input)
		}
	}
	for _, obsolete := range []string{"TEXT_MARKER:", "PATCH_BODY_LINE:", "SHELL_BODY_LINE:"} {
		if strings.Contains(toolGrammar, obsolete) {
			t.Errorf("obsolete framing remains: %s", obsolete)
		}
	}
}

func TestShellGrammarAllowsWhitespacePrograms(t *testing.T) {
	// Whitespace-only source remains valid; the router completes it as a no-op.
	inline := grammarTerminalRegexp(t, "SHELL_INLINE")
	for _, source := range []string{" ", "\t", " \t "} {
		if !inline.MatchString(source) {
			t.Errorf("shell grammar rejects whitespace-only source %q", source)
		}
	}
}

func TestShellInlineGrammar(t *testing.T) {
	// Match the complete line, including ordinary single-< redirections.
	inline := grammarTerminalRegexp(t, "SHELL_INLINE")
	token := regexp.MustCompile(strings.TrimSuffix(inline.String(), "$"))
	for _, command := range []string{"go test ./...", `rg -n 'TODO' src | head`, `echo "$(printf x)"`, "  echo x  ", "# comment", "世界", "printf '%s' 'a\rb'", "<", "cat < input", "echo '<'", "echo a<b", "echo > output"} {
		if !inline.MatchString(command) || token.FindString(command) != command {
			t.Errorf("inline grammar rejects or truncates %q", command)
		}
	}
	for _, invalid := range []string{"", "<<", "<<SHELL", "<<SHELLx", "<<SHELL ", "echo '<<'", "cat <<<text", "echo $((1<<2))", "echo x\nnew a", "echo x\r\n", "<<SHELL\r\n", "<<SHELL\n"} {
		if inline.MatchString(invalid) {
			t.Errorf("inline grammar accepts %q", invalid)
		}
	}
}

func TestValidateScriptSyntaxDoesNotEvaluate(t *testing.T) {
	if err := ValidateScriptSyntax("in missing\ntype 1:abcd \"value\""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScriptSyntax("shell <<SHELL\ntrue\nSHELL"); err == nil {
		t.Fatal("engine syntax accepted routed execution")
	}
}

func TestToolDescriptionIsNonInstructional(t *testing.T) {
	const want = "HPATCH/2 edits and mixed edit/command execution (Code Mode required for mixed scripts). Edit validation is atomic; failed host application may have partial effects."
	if got := ToolDescription(); got != want {
		t.Fatalf("ToolDescription() = %q, want %q", got, want)
	}
}

func TestMekugi2ToolDescriptionExamplesExecute(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "parser.go", "package parser\n\nfunc parse() {}\n", 0o644)
	script := "in parser.go\n" +
		"add " + row(3, "func parse() {}") + ` "// parse converts one command.\n"`
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "package parser\n\n// parse converts one command.\nfunc parse() {}\n"
	if got := readTestFile(t, root, "parser.go"); got != want {
		t.Fatalf("parser.go = %q, want %q", got, want)
	}
}

func grammarTerminalRegexp(t *testing.T, name string) *regexp.Regexp {
	t.Helper()
	prefix := name + ": /"
	for line := range strings.SplitSeq(toolGrammar, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		pattern, ok := strings.CutSuffix(strings.TrimPrefix(line, prefix), "/")
		if !ok {
			t.Fatalf("%s terminal does not end with /: %q", name, line)
		}
		pattern = strings.ReplaceAll(pattern, `\/`, "/")
		compiled, err := regexp.Compile("^(?:" + pattern + ")$")
		if err != nil {
			t.Fatalf("compile %s terminal: %v", name, err)
		}
		return compiled
	}
	t.Fatalf("%s terminal not found", name)
	return nil
}
