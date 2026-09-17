package shellsyntax

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	for _, test := range []struct {
		name, input string
		want        []string
	}{
		{"single unchanged", "#!python3\r\n\r\nprint('hello')\r\n", []string{"#!python3\r\n\r\nprint('hello')\r\n"}},
		{"implicit first Bash", "echo one\n#!python3\nprint(2)", []string{"echo one\n", "#!python3\nprint(2)"}},
		{"inherit params", "#!params={\"yield_time_ms\":1000}\necho one\n#!python3\nprint(2)",
			[]string{"#!params={\"yield_time_ms\":1000}\necho one\n", "#!python3\n#!params={\"yield_time_ms\":1000}\nprint(2)"}},
		{"replace params", "#!params={\"workdir\":\"/tmp\"}\necho one\n#!bash\n#!params={\"yield_time_ms\":2000}\necho two\n#!python3\nprint(3)",
			[]string{"#!params={\"workdir\":\"/tmp\"}\necho one\n", "#!bash\n#!params={\"yield_time_ms\":2000}\necho two\n", "#!python3\n#!params={\"yield_time_ms\":2000}\nprint(3)"}},
		{"clear params", "#!params={\"workdir\":\"/tmp\"}\necho one\n#!bash\n#!params={}\necho two\n#!python3\nprint(3)",
			[]string{"#!params={\"workdir\":\"/tmp\"}\necho one\n", "#!bash\n#!params={}\necho two\n", "#!python3\n#!params={}\nprint(3)"}},
		{"template not inherited", "#!cmd=producer | {.}\n#!params={}\ncat\n#!python3\r\nprint(2)\r\n",
			[]string{"#!cmd=producer | {.}\n#!params={}\ncat\n", "#!python3\r\n#!params={}\nprint(2)\r\n"}},
		{"bare CR", "#!params={}\recho one\r#!python3\rprint(2)",
			[]string{"#!params={}\recho one\r", "#!python3\r#!params={}\nprint(2)"}},
		{"same interpreter", "#!bash\necho one\n#!bash\necho two", []string{"#!bash\necho one\n", "#!bash\necho two"}},
		{"path and arguments", "echo one\n#!/usr/bin/python3 -u\nprint(2)", []string{"echo one\n", "#!/usr/bin/python3 -u\nprint(2)"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Split(test.input)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Split = %#v, %v; want %#v", got, err, test.want)
			}
		})
	}
}

func TestIsBatch(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		if !IsBatch("echo one" + ending + "#!python3" + ending + "print(2)") {
			t.Fatal("interpreter boundary not recognized")
		}
		for _, line := range []string{" #!python3", "\t#!bash", "#!params={}", "#!cmd=cat | {.}", "#!cmd missing", "#!unknown=value", "---"} {
			if IsBatch("echo one" + ending + line) {
				t.Fatalf("%q became a boundary", line)
			}
		}
	}
	if IsBatch("#!python3\nprint(2)") {
		t.Fatal("initial interpreter became a batch")
	}
}

func TestSplitPreservesShellConstructs(t *testing.T) {
	for _, source := range []string{
		"cat <<'EOF'\n#!python3\nEOF\n",
		"cat <<EOF\n#!python3\nEOF\n",
		"cat <<\\EOF\n#!python3\nEOF\n",
		"cat <<E\"OF\"\n#!python3\nEOF\n",
		"cat <<-'EOF'\n#!python3\n\tEOF\n",
		"cat <<FIRST <<'SECOND'\n#!python3\nFIRST\n#!bash\nSECOND\n",
		"cat <<'#!python3'\n#!bash\n#!python3\n",
		"printf '%s' '\n#!python3\n'\n",
		"printf '%s' \"\n#!python3\n\"\n",
		"if true; then\n#!python3\nprintf one\nfi\n",
		"echo \"$(cat <<'EOF'\n#!python3\nEOF\n)\"\n",
	} {
		for _, interpreter := range []string{"", "#!bash\n", "#!sh\n", "#!dash\n", "#!ash\n", "#!ksh\n", "#!mksh\n", "#!zsh\n"} {
			first := interpreter + source
			if IsBatch(first) {
				t.Errorf("literal header became a boundary: %q", first)
			}
			got, err := Split(first + "#!python3\nprint(2)")
			want := []string{first, "#!python3\nprint(2)"}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("Split(%q) = %#v, %v; want %#v", first, got, err, want)
			}
		}
	}
}

func TestSplitUnclosedHeredocDoesNotExposeAnotherProgram(t *testing.T) {
	source := "cat <<'EOF'\n#!python3\nprint(2)\n"
	got, err := Split(source)
	if err != nil || !reflect.DeepEqual(got, []string{source}) || IsBatch(source) {
		t.Fatalf("unclosed heredoc split: %#v, %v", got, err)
	}
}

func TestSplitKeepsSingleProgramSource(t *testing.T) {
	for _, input := range []string{
		"#!python3\nexample = '''\n #!python3\n#!params={bad}\n#!script=@shell/example\n#!batch=NEXT\nNEXT\n---\n'''\n",
		"cat <<'EOF'\n #!python3\n#!params={bad}\n#!script=@shell/example\nEOF\n",
		"echo one\n#!cmd=producer | {.}\necho two",
		"echo one\n#!params={bad}\necho two",
		"echo one\n#!params-file=example\necho two",
	} {
		got, err := Split(input)
		if err != nil || !reflect.DeepEqual(got, []string{input}) {
			t.Errorf("Split changed single program: %#v, %v", got, err)
		}
	}
}

func TestSplitRejectsInvalidPrograms(t *testing.T) {
	for _, source := range []string{
		"#!batch=NEXT\necho one\nNEXT\necho two",
		"#!batch-stop=NEXT\necho one\nNEXT\necho two",
		"#!bash\n#!python3\nprint(2)",
		"echo one\n#!bash",
		"echo one\n#!bash\n",
		"echo one\n#!bash\n#!python3\nprint(3)",
		"echo one\n#!bash\n#!params={bad}\necho two",
		"echo one\n#!python3\n",
		"#!script=@shell/example",
		"echo one\n#!bash\n#!script=@shell/example",
		"#!params={}\n#!params={}\necho one",
		"echo one\n#!\necho two",
		"echo one\n#!python3\nprint('\x00')",
	} {
		if programs, err := Split(source); err == nil {
			t.Errorf("Split(%q) = %#v, want rejection", source, programs)
		}
	}
}

func TestSplitHeaderErrorLocation(t *testing.T) {
	_, err := Split("echo first\n#!python3\n#!cmd missing\nprint(1)")
	if err == nil || !strings.Contains(err.Error(), "shell program 2: line 2:") {
		t.Fatalf("batch header error = %v, want program 2, line 2", err)
	}
}

func TestSplitHeredocLineEndings(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		first := strings.ReplaceAll("#!bash\ncat <<'EOF'\n#!python3\nEOF\n", "\n", ending)
		got, err := Split(first + "#!python3" + ending + "print(2)")
		want := []string{first, "#!python3" + ending + "print(2)"}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Split = %#v, %v; want %#v", got, err, want)
		}
	}
}

func BenchmarkSplitHeredocHeaders(b *testing.B) {
	for _, rows := range []int{1000, 5000, 10000} {
		b.Run(fmt.Sprint(rows), func(b *testing.B) {
			source := "cat <<'EOF'\n" + strings.Repeat("#!python3\n", rows) + "EOF\n#!python3\nprint(2)"
			b.SetBytes(int64(len(source)))
			b.ReportAllocs()
			for b.Loop() {
				programs, err := Split(source)
				if err != nil || len(programs) != 2 {
					b.Fatalf("Split = %d programs, %v", len(programs), err)
				}
			}
		})
	}
}

func TestSplitProtectsLaterProgramHeredocs(t *testing.T) {
	for _, header := range []string{"#!bash\n", "#!sh\n", "#!zsh\n"} {
		first := "printf one\n"
		second := "#!python3\nprint(2)\n"
		third := header + "cat <<'EOF'\n#!python3\nEOF\n"
		last := "#!python3\nprint(4)"
		got, err := Split(first + second + third + last)
		want := []string{first, second, third, last}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Split = %#v, %v; want %#v", got, err, want)
		}
	}
}

func BenchmarkSplitBashPrograms(b *testing.B) {
	for _, count := range []int{1000, 5000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			source := strings.Repeat("#!bash\necho x\n", count)
			b.SetBytes(int64(len(source)))
			b.ReportAllocs()
			for b.Loop() {
				programs, err := Split(source)
				if err != nil || len(programs) != count {
					b.Fatalf("Split = %d programs, %v", len(programs), err)
				}
			}
		})
	}
}
