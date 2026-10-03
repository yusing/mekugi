package router

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveDiffPythonExpansionRuntime(t *testing.T) {
	t.Parallel()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable for differential runtime validation")
	}
	// Use an actual source baseline, but execute only the fixed scripts below,
	// never commands discovered in source, history, or a corpus.
	source, err := os.ReadFile("live_diff_python_values.go")
	if err != nil {
		t.Fatal(err)
	}
	const read = "s = open('target.txt').read()\n"
	const write = "\nopen('target.txt', 'w').write(s)\n"
	for _, tc := range []struct {
		name, script, before, after string
		extra                       map[string]string
	}{
		{
			name:   "computed replacement on real Go source",
			script: read + "old = 'func ' + 'liveDiffPythonWhitespace'\nnew = 'func ' + 'previewPythonWhitespace'\ns = s.replace(old, new, 1)" + write,
			before: string(source),
			after:  strings.Replace(string(source), "func liveDiffPythonWhitespace", "func previewPythonWhitespace", 1),
		},
		{
			name:   "source derived replacement arguments",
			script: read + "old = s[:3]\nnew = s[4:7].lower()\ns = s.replace(old, new, 1)" + write,
			before: "old NEW old\n",
			after:  "new NEW old\n",
		},
		{
			name:   "CRLF read normalizes before Unicode len and slice",
			script: read + "s = s[:len(s)-1] + '!'" + write,
			before: "é🙂\r\nold\r\n",
			after:  "é🙂\nold!",
		},
		{
			name:   "CR read normalizes before find and replacement",
			script: read + "i = s.find('old')\ns = s[:i] + s[i:].replace('old', 'new')" + write,
			before: "é🙂\rold\r",
			after:  "é🙂\nnew\n",
		},
		{
			name:   "direct open empty newline preserves CRLF for len and slice",
			script: "s = open('target.txt', newline='').read()\ns = s[:len(s)-2] + '!'" + write,
			before: "é🙂\r\nold\r\n",
			after:  "é🙂\r\nold!",
		},
		{
			name:   "with handle empty newline preserves CR for find and slice",
			script: "with open('target.txt', newline='') as handle:\n    s = handle.read()\ni = s.find('\\r')\ns = s[:i] + '|' + s[i+1:len(s)-1]" + write,
			before: "é🙂\rold\r",
			after:  "é🙂|old",
		},
		{
			name:   "Path read_text default normalizes CR",
			script: "from pathlib import Path\ns = Path('target.txt').read_text()\ns = s.replace('old', 'new')" + write,
			before: "é🙂\rold\r",
			after:  "é🙂\nnew\n",
		},
		{
			name: "CR write translation composes with preserved read back",
			script: read + "open('target.txt', 'w', newline='\\r').write(s)\n" +
				"s = open('target.txt', newline='').read()\ns = s.replace('\\r', '|')" + write,
			before: "old\né🙂\n",
			after:  "old|é🙂|",
		},
		{
			name: "CRLF write handle translation composes with preserved read back",
			script: read + "with open('target.txt', 'w', newline='\\r\\n') as handle:\n    handle.write(s)\n" +
				"s = open('target.txt', newline='').read()\ns = s.replace('\\r\\n', '|')" + write,
			before: "old\né🙂\n",
			after:  "old|é🙂|",
		},
		{
			name:   "empty search Unicode boundaries",
			script: read + "s = s.replace('', '|')" + write,
			before: "é🙂",
			after:  "|é|🙂|",
		},
		{
			name:   "empty search bounded count",
			script: read + "s = s.replace('', '|', 2)" + write,
			before: "é🙂",
			after:  "|é|🙂",
		},
		{
			name:   "Unicode len find indexes and negative slices",
			script: read + "i = s.find('a')\ns = s[:i] + s[-2] + s[1:len(s)-1] + s[-99:2] + s[99:]" + write,
			before: "é🙂a\n",
			after:  "é🙂a🙂aé🙂",
		},
		{
			name:   "strip split and join Unicode whitespace",
			script: read + "s = '|'.join(s.strip().split())" + write,
			before: "\u2003 é\t 🙂  x \u00a0\n",
			after:  "é|🙂|x",
		},
		{
			name:   "strip character set and delimiter split",
			script: read + "s = '|'.join(s.strip('xy').split(','))" + write,
			before: "xy,é,,🙂,yx",
			after:  "|é||🙂|",
		},
		{
			name:   "splitlines Unicode and terminal boundaries",
			script: read + "s = '|'.join(s.splitlines())" + write,
			before: "é\n🙂\vX\fY\u0085Z\u2028Q\u2029\n",
			after:  "é|🙂|X|Y|Z|Q|",
		},
		{
			name:   "repetition operand orders and nonpositive counts",
			script: "s = 2 * 'é' + '🙂' * 3 + 'x' * -2 + 'y' * 0" + write,
			before: "old\n",
			after:  "éé🙂🙂🙂",
		},
		{
			name: "helper list keyword parameter and pair iteration",
			script: "pairs = [('old', 'middle'), ('middle', 'new')]\n" +
				"def edit(pairs):\n    s = open('target.txt').read()\n    for old, new in pairs:\n        s = s.replace(old, new)\n    open('target.txt', 'w').write(s)\nedit(pairs=pairs)\n",
			before: "old é🙂 old\n",
			after:  "new é🙂 new\n",
		},
		{
			name:   "helper tuple default and join",
			script: "def edit(parts=('é', '', '🙂')):\n    open('target.txt', 'w').write('-'.join(parts))\nedit()\n",
			before: "old\n",
			after:  "é--🙂",
		},
		{
			name: "helper dictionary parameter items and caller scope",
			script: "pairs = {'old': 'global'}\n" +
				"def edit(pairs):\n    s = open('target.txt').read()\n    for old, new in pairs.items():\n        s = s.replace(old, new)\n    pairs = {'old': 'local'}\n    open('target.txt', 'w').write(s)\n" +
				"edit({'old': 'argument'})\nfor old, new in pairs.items():\n    open('global.txt', 'w').write(new)\n",
			before: "old é🙂\n",
			after:  "argument é🙂\n",
			extra:  map[string]string{"global.txt": "global"},
		},
		{
			name:   "dictionary duplicate keys preserve iteration order",
			script: "pairs = {'b': 'first', 'a': 'second', 'b': 'last'}\ns = ''\nfor key in pairs:\n    s += key\nfor key, value in pairs.items():\n    s += key + ':' + value + ';'" + write,
			before: "old\n",
			after:  "bab:last;a:second;",
		},
		{
			name:   "completed triple quote preserves trailing blank source lines",
			script: read + "s = s + '''\n\n'''" + write,
			before: string(source),
			after:  string(source) + "\n\n",
		},
		{
			name:   "one write handle appends loop writes including empty strings",
			script: "with open('target.txt', 'w') as handle:\n    for part in ['é', '', '🙂']:\n        handle.write(part)\n    handle.write('!')\n",
			before: "old\n",
			after:  "é🙂!",
		},
		{
			name:   "read handle loop advances to EOF",
			script: "s = ''\nwith open('target.txt') as handle:\n    for part in ['a', 'b']:\n        s += handle.read()\ns = 'seen:' + s" + write,
			before: "é🙂\n",
			after:  "seen:é🙂\n",
		},
		{
			name:   "repeat integer operand consumes before string operand",
			script: "with open('target.txt') as handle:\n    s = len(handle.read()) * (handle.read() + '!')" + write,
			before: "old",
			after:  "!!!",
		},
		{
			name:   "repeat string operand consumes before integer operand",
			script: "with open('target.txt') as handle:\n    s = (handle.read() + '!') * len(handle.read())\ns += 'tail'" + write,
			before: "old",
			after:  "tail",
		},
		{
			name:   "read handle straight line advances to EOF",
			script: "with open('target.txt') as handle:\n    first = handle.read()\n    second = handle.read()\ns = first + '|' + second" + write,
			before: "é🙂\n",
			after:  "é🙂\n|",
		},
		{
			name:   "two read nodes in one expression execute left to right",
			script: "with open('target.txt') as handle:\n    s = handle.read() + '|' + handle.read()" + write,
			before: "é🙂\n",
			after:  "é🙂\n|",
		},
		{
			name:   "read positional helper arguments execute left to right",
			script: "def edit(first, second):\n    open('output.txt', 'w').write(first + '|' + second)\nwith open('target.txt') as handle:\n    first = handle.read()\n    edit(first, handle.read())\n",
			before: "é🙂\n",
			after:  "é🙂\n",
			extra:  map[string]string{"output.txt": "é🙂\n|"},
		},
		{
			name:   "two read positional helper arguments consume once each",
			script: "def edit(first, second):\n    open('output.txt', 'w').write(first + '|' + second)\nwith open('target.txt') as handle:\n    edit(handle.read(), handle.read())\n",
			before: "é🙂\n",
			after:  "é🙂\n",
			extra:  map[string]string{"output.txt": "é🙂\n|"},
		},
		{
			name:   "read keyword helper arguments follow source not parameter order",
			script: "def edit(first, second):\n    open('output.txt', 'w').write(first + '|' + second)\nwith open('target.txt') as handle:\n    edit(second=handle.read(), first=handle.read())\n",
			before: "é🙂\n",
			after:  "é🙂\n",
			extra:  map[string]string{"output.txt": "|é🙂\n"},
		},
		{
			name:   "repeated helpers append through global write handle",
			script: "def emit(part):\n    handle.write(part)\nwith open('target.txt', 'w') as handle:\n    emit('é')\n    emit('')\n    emit('🙂')\n",
			before: "old\n",
			after:  "é🙂",
		},
		{
			name:   "new write handle truncates previous accumulated writes",
			script: "with open('target.txt', 'w') as handle:\n    handle.write('discard')\n    handle.write('this')\nwith open('target.txt', 'w') as handle:\n    handle.write('é')\n    handle.write('')\n    handle.write('🙂')\n",
			before: "old\n",
			after:  "é🙂",
		},
		{
			name:   "text default captures definition binding",
			script: "text = 'captured'\ndef edit(value=text):\n    open('target.txt', 'w').write(value)\ntext = 'rebound'\nedit()\n",
			before: "old\n",
			after:  "captured",
		},
		{
			name:   "default evaluates before function shadows its own name",
			script: "edit = 'captured'\ndef edit(value=edit):\n    open('target.txt', 'w').write(value)\nedit()\n",
			before: "old\n",
			after:  "captured",
		},
		{
			name:   "list default captures definition binding",
			script: "parts = ['é', '🙂']\ndef edit(values=parts):\n    open('target.txt', 'w').write(''.join(values))\nparts = ['rebound']\nedit()\n",
			before: "old\n",
			after:  "é🙂",
		},
		{
			name:   "dictionary default captures definition binding",
			script: "pairs = {'old': 'captured'}\ndef edit(values=pairs):\n    s = open('target.txt').read()\n    for old, new in values.items():\n        s = s.replace(old, new)\n    open('target.txt', 'w').write(s)\npairs = {'old': 'rebound'}\nedit()\n",
			before: "old\n",
			after:  "captured\n",
		},
		{
			name:   "path default captures definition binding",
			script: "from pathlib import Path\npath = Path('target.txt')\ndef edit(destination=path):\n    destination.write_text('captured')\npath = Path('other.txt')\nedit()\n",
			before: "old\n",
			after:  "captured",
		},
		{
			name:   "integer default captures definition binding",
			script: "n = 2\ndef edit(count=n):\n    s = open('target.txt').read()\n    open('target.txt', 'w').write(s[:count])\nn = 4\nedit()\n",
			before: "é🙂ab\n",
			after:  "é🙂",
		},
		{
			name: "explicit arguments override all captured default value kinds",
			script: "from pathlib import Path\ntext = 'old'\nparts = ['wrong']\npairs = {'old': 'wrong'}\npath = Path('other.txt')\nn = 1\n" +
				"def edit(text=text, parts=parts, pairs=pairs, path=path, n=n):\n    for old, new in pairs.items():\n        text = text.replace(old, new)\n    path.write_text(text[:n] + '|' + ''.join(parts))\n" +
				"text = 'rebound'\nparts = ['rebound']\npairs = {'old': 'rebound'}\npath = Path('rebound.txt')\nn = 0\n" +
				"edit(text='old', parts=['é', '🙂'], pairs={'old': 'NEW'}, path=Path('target.txt'), n=3)\n",
			before: "old\n",
			after:  "NEW|é🙂",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			previewDirectory, runtimeDirectory := t.TempDir(), t.TempDir()
			before := map[string]string{"target.txt": tc.before, "untouched.txt": "sentinel é🙂\n"}
			after := map[string]string{"target.txt": tc.after, "untouched.txt": before["untouched.txt"]}
			for name, text := range tc.extra {
				after[name] = text
			}
			for _, directory := range []string{previewDirectory, runtimeDirectory} {
				for name, text := range before {
					writeTestFile(t, filepath.Join(directory, name), text)
				}
			}
			worker := liveDiffPreviewWorker{ctx: t.Context()}
			files, recognized, err := worker.projectShell("python3 - <<'PY'\n"+tc.script+"PY\n", previewDirectory, true)
			if err != nil || !recognized {
				t.Fatalf("completed preview recognized=%v err=%v files=%+v", recognized, err, files)
			}
			// Prove isolation before executing Python, even when the later
			// differential assertion discovers an incorrect prediction.
			entries, err := os.ReadDir(previewDirectory)
			if err != nil || len(entries) != len(before) {
				t.Fatalf("preview changed directory entries: entries=%v err=%v", entries, err)
			}
			for name, want := range before {
				got, err := os.ReadFile(filepath.Join(previewDirectory, name))
				if err != nil || string(got) != want {
					t.Fatalf("preview mutated %q: got=%q err=%v want=%q", name, got, err, want)
				}
			}

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, python, "-I", "-X", "utf8", "-c", tc.script)
			command.Dir = runtimeDirectory
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("trusted Python fixture failed: %v\n%s", err, output)
			}
			entries, err = os.ReadDir(runtimeDirectory)
			if err != nil {
				t.Fatal(err)
			}
			actual := make(map[string]string, len(entries))
			for _, entry := range entries {
				text, err := os.ReadFile(filepath.Join(runtimeDirectory, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				actual[entry.Name()] = string(text)
			}
			if len(actual) != len(after) {
				t.Fatalf("Python wrote %d targets, want %d", len(actual), len(after))
			}
			for name, want := range after {
				if got, exists := actual[name]; !exists || got != want {
					t.Fatalf("Python target %q: exists=%v got=%q want=%q", name, exists, got, want)
				}
			}
			// Compare the preview against actual Python output, not just the
			// hand-authored expectation, and recheck preview isolation.
			assertPythonExpansionDiffs(t, previewDirectory, files, before, actual)
		})
	}
}

func TestLiveDiffPythonExpansionRejectsComputedOrInvalidNewline(t *testing.T) {
	for name, script := range map[string]string{
		"bound read setting":       "newline = ''\ns = open('target.txt', newline=newline).read()\nopen('target.txt', 'w').write(s + '!')\n",
		"computed with setting":    "with open('target.txt', newline='\\r' + '\\n') as handle:\n    s = handle.read()\nopen('target.txt', 'w').write(s + '!')\n",
		"invalid read string":      "s = open('target.txt', newline='invalid').read()\nopen('target.txt', 'w').write(s + '!')\n",
		"invalid numeric setting":  "open('target.txt', 'w', newline=0).write('new')\n",
		"computed write setting":   "open('target.txt', 'w', newline='\\r' + '\\n').write('new')\n",
		"duplicate write settings": "open('target.txt', 'w', newline='', newline='\\n').write('new')\n",
	} {
		t.Run(name, func(t *testing.T) {
			// These intentionally unsupported or invalid settings are never
			// executed. Validate fail-closed prediction and filesystem isolation.
			runPythonExpansion(t, script, map[string]string{"target.txt": "é🙂\rold\r"}, nil)
		})
	}
}

func TestLiveDiffPythonCorpusPathReplaceRetainsEditIntent(t *testing.T) {
	commands, uncertain := pythonCorpusExtract(pythonCorpusCommand{
		directory: t.TempDir(),
		command:   "python3 - <<'PY'\nfrom pathlib import Path\nPath('old.txt').replace('new.txt')\nPY\n",
	})
	if uncertain != 0 || len(commands) != 1 {
		t.Fatalf("got %d invocations, %d uncertain; want one confirmed invocation", len(commands), uncertain)
	}
	if category := commands[0].category; category != "file write" && category != "unknown edit intent" {
		t.Fatalf("Path.replace category=%q, want retained file write or unknown edit intent", category)
	}
}

func TestLiveDiffPythonExpansionRejectsClosedOrOverlappingHandles(t *testing.T) {
	for name, script := range map[string]string{
		"closed write handle": "with open('target.txt', 'w') as handle:\n    handle.write('discard')\nhandle.write('closed')\n",
		"overlapping writers": "with open('target.txt', 'w') as first:\n    first.write('first')\n    with open('target.txt', 'w') as second:\n        second.write('second')\n    first.write('last')\n",
	} {
		t.Run(name, func(t *testing.T) {
			// Do not execute invalid or deliberately unsupported handle use.
			runPythonExpansion(t, script, map[string]string{"target.txt": "old é🙂\n"}, nil)
		})
	}
}

func TestLiveDiffPythonExpansionRejectsHiddenActiveHandlesAndStaleBindings(t *testing.T) {
	for name, script := range map[string]string{
		"rebound writer remains active":             "with open('target.txt', 'w') as handle:\n    handle = 'rebound'\n    with open('target.txt', 'w') as second:\n        second.write('new')\n",
		"helper shadow does not hide active writer": "def edit(handle):\n    with open('target.txt', 'w') as second:\n        second.write('new')\nwith open('target.txt', 'w') as handle:\n    edit('shadow')\n",
		"with handle invalidates old text":          "handle = 'borrowed'\nwith open('target.txt') as handle:\n    s = handle + '!'\nopen('target.txt', 'w').write(s)\n",
		"with handle invalidates old list":          "handle = ['borrowed']\nwith open('target.txt') as handle:\n    s = ''.join(handle)\nopen('target.txt', 'w').write(s)\n",
	} {
		t.Run(name, func(t *testing.T) {
			runPythonExpansion(t, script, map[string]string{"target.txt": "old é🙂\n"}, nil)
		})
	}
}
