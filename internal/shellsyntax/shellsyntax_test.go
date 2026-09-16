package shellsyntax

import (
	"fmt"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	parsed, err := Parse("#!/usr/bin/env -S python3 -u\r\n#!params={\"tty\":true}\r\n#!cmd=wrap {.}\r\nprint('ok')\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Interpreter) != 2 || parsed.Interpreter[0] != "python3" || parsed.Interpreter[1] != "-u" ||
		parsed.CommandTemplate != "wrap {.}" || !parsed.HasParams || parsed.ParamsLine != 2 || parsed.Body != "print('ok')\r\n" {
		t.Fatalf("Parse = %+v", parsed)
	}

	retained, err := Parse("#!script=@shell/call-id")
	if err != nil || retained.ScriptPath != "@shell/call-id" {
		t.Fatalf("retained Parse = %+v, %v", retained, err)
	}
}

func TestParseFailures(t *testing.T) {
	for _, input := range []string{
		"#!\nbody",
		"#!/usr/bin/env -S\nbody",
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
		{"#!python3\n#!cmd missing\nprint(1)", 2},
		{"#!python3\r\n#!cmd={.}\r\n#!params={bad}\r\nprint(1)", 3},
		{"#!params={}\r#!params={}\recho one", 2},
		{"#!script=@shell/example\necho one", 2},
		{"#!python3\nprint(1)\n\x00", 3},
	} {
		_, err := Parse(test.source)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("line %d:", test.line)) {
			t.Errorf("Parse(%q) error = %v, want line %d", test.source, err, test.line)
		}
	}
}
