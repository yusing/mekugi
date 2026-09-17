package shellsyntax

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	for _, test := range []struct {
		name, input string
		want        []string
	}{
		{
			name:  "single unchanged",
			input: "#!python3\r\n\r\nprint('hello')\r\n",
			want:  []string{"#!python3\r\n\r\nprint('hello')\r\n"},
		},
		{
			name:  "mixed interpreters inherit",
			input: "#!batch=NEXT\n#!params={\"yield_time_ms\":1000}\necho hello\nNEXT\n#!python3\nprint('hello')\n",
			want: []string{
				"#!params={\"yield_time_ms\":1000}\necho hello\n",
				"#!python3\n#!params={\"yield_time_ms\":1000}\nprint('hello')\n",
			},
		},
		{
			name:  "implicit bash replaces params",
			input: "#!batch=NEXT\n#!params={\"workdir\":\"/tmp\",\"tty\":false}\necho one\nNEXT\n#!params={\"yield_time_ms\":2000}\necho two\nNEXT\n#!python3\nprint(3)",
			want: []string{
				"#!params={\"workdir\":\"/tmp\",\"tty\":false}\necho one\n",
				"#!params={\"yield_time_ms\":2000}\necho two\n",
				"#!python3\n#!params={\"yield_time_ms\":2000}\nprint(3)",
			},
		},
		{
			name:  "new params after selector",
			input: "#!batch=NEXT\necho one\nNEXT\n#!python3\n#!params={}\nprint(2)\nNEXT\n#!bash\necho three",
			want: []string{
				"echo one\n",
				"#!python3\n#!params={}\nprint(2)\n",
				"#!bash\n#!params={}\necho three",
			},
		},
		{
			name:  "empty params clears inheritance",
			input: "#!batch=NEXT\n#!params={\"workdir\":\"/tmp\"}\necho one\nNEXT\n#!params={}\necho two\nNEXT\n#!python3\nprint(3)",
			want: []string{
				"#!params={\"workdir\":\"/tmp\"}\necho one\n",
				"#!params={}\necho two\n",
				"#!python3\n#!params={}\nprint(3)",
			},
		},
		{
			name:  "template not inherited",
			input: "#!batch=NEXT\n#!cmd=producer | {.}\n#!params={}\ncat\nNEXT\n#!python3\r\nprint(2)\r\n",
			want: []string{
				"#!cmd=producer | {.}\n#!params={}\ncat\n",
				"#!python3\r\n#!params={}\nprint(2)\r\n",
			},
		},
		{
			name:  "bare CR",
			input: "#!batch=NEXT\r#!params={}\recho one\rNEXT\r#!python3\rprint(2)",
			want:  []string{"#!params={}\recho one\r", "#!python3\r#!params={}\nprint(2)"},
		},
		{
			name:  "separator matches exact whole line",
			input: "#!batch=NEXT\r\necho one\r\n NEXT\r\nNEXT suffix\r\nNEXT \r\nNEXT\r\nprintf two",
			want:  []string{"echo one\r\n NEXT\r\nNEXT suffix\r\nNEXT \r\n", "printf two"},
		},
		{
			name:  "caller chooses another separator for examples",
			input: "#!batch=--another--\n#!python3\nexample = '''\nNEXT\n#!batch=NEXT\n#!python3\n#!params={}\n'''\n--another--\nprintf two",
			want:  []string{"#!python3\nexample = '''\nNEXT\n#!batch=NEXT\n#!python3\n#!params={}\n'''\n", "printf two"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Split(test.input)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Split = %#v, %v; want %#v", got, err, test.want)
			}
		})
	}
}

func TestBatchStopPolicyPreservesPrograms(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		source := "#!batch-stop=NEXT" + ending + "#!params={\"yield_time_ms\":1000}" + ending +
			"exit 7" + ending + "NEXT" + ending + "#!python3" + ending + "print(2)"
		separator, stop, batch := BatchHeader(source)
		if separator != "NEXT" || !stop || !batch {
			t.Fatalf("batch header = %q, %v, %v", separator, stop, batch)
		}
		got, err := Split(source)
		want, wantErr := Split(strings.Replace(source, "#!batch-stop=", "#!batch=", 1))
		if err != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("policy changed program contents: %#v, %v; want %#v, %v", got, err, want, wantErr)
		}
	}
	for _, source := range []string{
		"#!batch-stop=\necho one\nNEXT\necho two",
		"#!batch-stop=NEXT\nexit 7\nNEXT\n#!params={bad}\necho two",
		"#!batch-stop=NEXT\nexit 7",
	} {
		if _, err := Split(source); err == nil {
			t.Fatalf("invalid stop batch accepted: %q", source)
		}
	}
	source := "#!python3\nexample = '''\n#!batch-stop=NEXT\nNEXT\n'''\n"
	if separator, stop, batch := BatchHeader(source); separator != "" || stop || batch {
		t.Fatal("body example became a batch")
	}
}

func TestSplitKeepsSingleProgramSource(t *testing.T) {
	for _, input := range []string{
		"#!python3\nexample = '''\n#!python3\n#!params={bad}\n#!script=@shell/example\n#!batch=NEXT\nNEXT\n'''\n",
		"cat <<'EOF'\n#!python3\n#!params={bad}\n#!script=@shell/example\nEOF\n",
		"#!bash\n#!python3\nprintf one",
		"echo one\n#!cmd=producer | {.}\necho two",
		"echo one\n#!params={bad}\necho two",
		"echo one\n#!\necho two",
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
		"#!batch=\necho one\nNEXT\necho two",
		"#!batch= \necho one",
		"#!batch= NEXT\necho one",
		"#!batch=NEXT \necho one",
		"#!batch=N\x00EXT\necho one\nN\x00EXT\necho two",
		"#!batch=NEXT\necho one",
		"#!batch=NEXT\nNEXT\necho two",
		"#!batch=NEXT\necho one\nNEXT",
		"#!batch=NEXT\necho one\nNEXT\n",
		"#!batch=NEXT\necho one\nNEXT\nNEXT\necho three",
		"#!batch=NEXT\necho one\nNEXT\n#!params={bad}\necho two",
		"#!batch=NEXT\necho one\nNEXT\n#!python3\n",
		"#!script=@shell/example",
		"#!batch=NEXT\necho one\nNEXT\n#!script=@shell/example",
		"#!params={}\n#!params={}\necho one",
		"#!batch=NEXT\necho one\nNEXT\n#!\necho two",
		"#!batch=NEXT\necho one\nNEXT\n#!python3\nprint('\x00')",
	} {
		if programs, err := Split(source); err == nil {
			t.Errorf("Split(%q) = %#v, want rejection", source, programs)
		}
	}
}

func TestSplitHeaderErrorLocation(t *testing.T) {
	_, err := Split("#!batch=NEXT\necho first\nNEXT\n#!python3\n#!cmd missing\nprint(1)")
	if err == nil || !strings.Contains(err.Error(), "shell program 2: line 2:") {
		t.Fatalf("batch header error = %v, want program 2, line 2", err)
	}
}
