package hpatchsyntax

import (
	"strings"
	"testing"
)

func TestSplitShell(t *testing.T) {
	source := "new a\n" +
		"type <<PATCH\nshell <<SHELL\nSHELL\nPATCH\n" +
		"shell <<SHELL\r\n#!python3\r\nprint('PATCH')\r\nSHELL\r\n" +
		"in a\ntype \"a\" \"b\"\n" +
		"shell <<SHELL\nprintf '%s' 'type <<PATCH'\nSHELL"
	parts, mixed, err := SplitShell(source)
	if err != nil || !mixed || len(parts) != 4 {
		t.Fatalf("split = %#v, %v, %v", parts, mixed, err)
	}
	if parts[0].Shell || !strings.Contains(parts[0].Source, "shell <<SHELL\nSHELL\nPATCH\n") ||
		!parts[1].Shell || parts[1].Source != "#!python3\r\nprint('PATCH')\r\n" ||
		parts[1].Line != 6 || parts[2].Shell || !parts[3].Shell {
		t.Fatalf("segments = %#v", parts)
	}
}

func TestSplitShellAllowsEmptyPrograms(t *testing.T) {
	for _, source := range []string{"shell", "shell ", "shell \t", "shell <<SHELL\nSHELL", "shell <<SHELL\n \nSHELL"} {
		parts, mixed, err := SplitShell(source)
		if err != nil || !mixed || len(parts) != 1 || !parts[0].Shell || strings.TrimSpace(parts[0].Source) != "" {
			t.Errorf("empty program %q: parts=%+v mixed=%v err=%v", source, parts, mixed, err)
		}
	}
}

func TestSplitShellRejectsMalformedFrames(t *testing.T) {
	for _, source := range []string{
		"shell \xff",
		"shell <<SHELL",
		"shell <<SHELL\nprintf x",
		"shell <<SHELL\n\xff\nSHELL",
		"shell <<SHELL\n" + strings.Repeat("x", MaxHeredocBodyBytes) + "\nSHELL",
		"shell <<SHELL\ntrue\nSHELL\nnew a\ntype <<PATCH\nunterminated",
	} {
		if _, mixed, err := SplitShell(source); !mixed || err == nil {
			t.Errorf("accepted malformed frame of %d bytes: mixed=%v err=%v", len(source), mixed, err)
		}
	}
}

func TestSplitShellKeepsOrdinaryDiagnostics(t *testing.T) {
	for _, source := range []string{
		"new a\ntype <<PATCH\nshell <<SHELL\nunterminated",
		"new a\ntype <<TEXT\n|shell <<SHELL\n|SHELL\nTEXT\n",
		"new a\ntype \"shell <<SHELL\\nSHELL\\n\"",
	} {
		if _, mixed, _ := SplitShell(source); mixed {
			t.Errorf("edit payload became shell: %q", source)
		}
	}
}

func TestSplitShellPreservesBareCarriageReturns(t *testing.T) {
	body := "printf 'a\rb'\nSHELL\rnot-a-close\n"
	parts, mixed, err := SplitShell("shell <<SHELL\n" + body + "SHELL\r\n")
	if err != nil || !mixed || len(parts) != 1 || parts[0].Source != body {
		t.Fatalf("split = %#v, %v, %v", parts, mixed, err)
	}
}

func TestSplitShellInline(t *testing.T) {
	source := "shell  printf '%s\\n' 'type \"literal\"' | cat  \r\n" +
		"new a\ntype <<PATCH\nshell echo data\nPATCH\n" +
		"shell <<SHELL\nprintf 'block'\nSHELL\n" +
		"shell echo last"
	parts, mixed, err := SplitShell(source)
	if err != nil || !mixed || len(parts) != 4 {
		t.Fatalf("split = %#v, %v, %v", parts, mixed, err)
	}
	if parts[0].Source != " printf '%s\\n' 'type \"literal\"' | cat  " || parts[0].Line != 1 || !parts[0].Shell ||
		parts[1].Shell || !strings.Contains(parts[1].Source, "shell echo data\n") ||
		parts[2].Source != "printf 'block'\n" || parts[2].Line != 6 ||
		parts[3].Source != "echo last" || parts[3].Line != 9 || !parts[3].Shell {
		t.Fatalf("segments = %#v", parts)
	}
	for _, command := range []string{"<", "cat < input", "echo '<'", "echo a<b", "echo > output"} {
		parts, mixed, err := SplitShell("shell " + command)
		if err != nil || !mixed || len(parts) != 1 || parts[0].Source != command {
			t.Errorf("inline %q = %#v, %v, %v", command, parts, mixed, err)
		}
	}
}

func TestSplitShellInlineDoesNotConsumeNextLine(t *testing.T) {
	parts, mixed, err := SplitShell("shell printf '%s' \\\nnew a\ntype \"value\"")
	if err != nil || !mixed || len(parts) != 2 || parts[0].Source != "printf '%s' \\" || parts[1].Shell {
		t.Fatalf("split = %#v, %v, %v", parts, mixed, err)
	}
}

func TestSplitShellInlinePreservesCarriageReturns(t *testing.T) {
	command := "printf '%s' 'a\rb'"
	parts, mixed, err := SplitShell("shell " + command + "\r\nshell echo next")
	if err != nil || !mixed || len(parts) != 2 || parts[0].Source != command ||
		parts[1].Source != "echo next" || parts[1].Line != 2 {
		t.Fatalf("split = %#v, %v, %v", parts, mixed, err)
	}
}

func TestSplitShellInlineRejectsDoubleLess(t *testing.T) {
	for _, command := range []string{"echo <<SHELL", "echo '<<'", "cat <<<text", "echo $((1<<2))"} {
		_, mixed, err := SplitShell("shell " + command)
		if !mixed || err == nil || !strings.Contains(err.Error(), "<< is not allowed") {
			t.Errorf("inline %q: mixed=%v err=%v", command, mixed, err)
		}
	}
}

func TestSplitShellHeredocDelimiters(t *testing.T) {
	for _, marker := range []string{"<<END", "<<'END'", "<<E'N'D", `<<\END`, "<<-END"} {
		source := "shell " + marker + "\n\tprintf '%s' '$HOME'\nEND\nnew a\ntype \"ok\""
		parts, mixed, err := SplitShell(source)
		want := "\tprintf '%s' '$HOME'\n"
		if marker == "<<-END" {
			want = strings.TrimPrefix(want, "\t")
		}
		if err != nil || !mixed || len(parts) != 2 || parts[0].Source != want || parts[1].Line != 4 {
			t.Fatalf("split = %+v, %v, %v", parts, mixed, err)
		}
	}
}
