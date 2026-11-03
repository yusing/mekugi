package shellsyntax

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/commandargs"
	"mvdan.cc/sh/v3/syntax"
)

func TestLiteralArgumentQuotes(t *testing.T) {
	for _, value := range []string{"", "can't", "line one\nline 'two'\t\\", "$HOME $(exit 1) *?[", "a\"b"} {
		for _, quote := range []func(string) string{commandargs.Quote, commandargs.QuoteArgument} {
			program, err := syntax.NewParser().Parse(strings.NewReader("cmd "+quote(value)), "")
			if err != nil {
				t.Fatal(err)
			}
			word := program.Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
			if got, ok := Literal(word); !ok || got != value {
				t.Errorf("Literal(%s) = %q, %v; want %q", quote(value), got, ok, value)
			}
			if got, ok := Args([]*syntax.Word{word}); !ok || len(got) != 1 || got[0] != value {
				t.Errorf("Args(%s) = %q, %v; want %q", quote(value), got, ok, value)
			}
		}
	}
	for _, source := range []string{"$HOME", "$(exit 1)", "`exit 1`", "\"$HOME\"", "~user"} {
		program, err := syntax.NewParser().Parse(strings.NewReader("cmd "+source), "")
		if err != nil {
			t.Fatal(err)
		}
		word := program.Stmts[0].Cmd.(*syntax.CallExpr).Args[1]
		if value, ok := Literal(word); ok {
			t.Errorf("dynamic Literal(%s) = %q", source, value)
		}
		if value, ok := Args([]*syntax.Word{word}); ok {
			t.Errorf("dynamic Args(%s) = %q", source, value)
		}
	}
}

func TestParse(t *testing.T) {
	parsed, err := Parse("#!/usr/bin/env -S python3 -u\r\n#!params={\"tty\":true}\r\nprint('ok')\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Interpreter) != 2 || parsed.Interpreter[0] != "python3" || parsed.Interpreter[1] != "-u" ||
		!parsed.HasParams || parsed.ParamsLine != 2 || parsed.Body != "print('ok')\r\n" {
		t.Fatalf("Parse = %+v", parsed)
	}

	if _, err := Parse("#!script=@shell/call-id"); err == nil || !strings.Contains(err.Error(), "unsupported shell directive #!script") {
		t.Fatalf("removed directive = %v", err)
	}
}

func TestParseFailures(t *testing.T) {
	for _, input := range []string{
		"#!\nbody",
		"#!/usr/bin/env -S\nbody",
		"#!cmd=producer | {.}\nbody",
		"#!params={}\n#!cmd={.}\nbody",
		"#!cmd=missing-placeholder\nbody",
		"#!params=[]\nbody",
		"#!unknown=value\nbody",
		"#!script=path\nbody",
	} {
		if _, err := Parse(input); err == nil {
			t.Errorf("Parse(%q) succeeded", input)
		}
	}
}

func TestParseRejectsDuplicateJSONParams(t *testing.T) {
	if _, err := Parse("#!params={\"tty\":true,\"tty\":false}\necho duplicate"); err == nil {
		t.Fatal("Parse accepted duplicate JSON parameter names")
	}
}
func TestInterpreterIdentity(t *testing.T) {
	for input, want := range map[string]string{
		"/usr/bin/BASH":       "bash",
		`C:\\Tools\\Node.EXE`: "node",
		"python3":             "python3",
	} {
		if got := InterpreterIdentity(input); got != want {
			t.Errorf("InterpreterIdentity(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHeaderErrorLocations(t *testing.T) {
	for _, test := range []struct {
		source string
		line   int
	}{
		{"#!", 1},
		{"#!/usr/bin/env", 1},
		{"#!python3\n#!params=\nprint(1)", 2},
		{"#!python3\r\n#!params={bad}\r\nprint(1)", 2},
		{"#!params={}\r#!params={}\recho one", 2},
		{"#!script=@shell/example\necho one", 1},
		{"#!python3\nprint(1)\n\x00", 3},
	} {
		_, err := Parse(test.source)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("line %d:", test.line)) {
			t.Errorf("Parse(%q) error = %v, want line %d", test.source, err, test.line)
		}
	}
}
