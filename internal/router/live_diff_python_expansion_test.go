package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestLiveDiffPythonExpansionKnownExpressions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, before, after string
	}{
		{"replace concatenated search and replacement", "s = s.replace('o' + 'ld', 'n' + 'ew', 1)", "old old\n", "new old\n"},
		{"replace source derived slices", "old = s[:3]\nnew = s[4:7]\ns = s.replace(old, new, 1)", "old NEW old\n", "NEW NEW old\n"},
		{"replace bound transformed text", "old = ' OLD '.strip().lower()\nnew = 'new'.upper()\ns = s.replace(old, new)", "old old\n", "NEW NEW\n"},
		{"len read buffer uses codepoints", "s = s[:len(s)-1] + '!'", "é🙂a\n", "é🙂a!"},
		{"len transformed expression", "s = s[:len(' é🙂 '.strip() + 'x')]", "abcd\n", "abc"},
		{"unicode negative indexes", "s = s[-2] + s[0] + s[1]", "é🙂a\n", "aé🙂"},
		{"unicode slices and negative bounds", "s = s[1:-1] + s[-99:2] + s[99:]", "é🙂a\n", "🙂aé🙂"},
		{"reversed bounds produce empty slice", "s = s[3:1] + 'empty'", "é🙂a\n", "empty"},
		{"unicode find offsets", "i = s.find('a')\ns = s[:i] + 'X' + s[i+1:]", "é🙂a\n", "é🙂X\n"},
		{"find missing preserves negative slice semantics", "i = s.find('missing')\ns = s[:i] + '!'", "é🙂a\n", "é🙂a!"},
		{"unicode empty find", "i = s.find('')\ns = s[i:] + '!'", "é🙂\n", "é🙂\n!"},
		{"strip whitespace", "s = s.strip()", " \té🙂\r\n", "é🙂"},
		{"lstrip preserves right whitespace", "s = s.lstrip()", " \té🙂 \n", "é🙂 \n"},
		{"rstrip preserves left whitespace", "s = s.rstrip()", " \té🙂 \n", " \té🙂"},
		{"strip characters are a set not a prefix", "chars = 'xy'\ns = s.strip(chars)", "xyyxbodyyxx", "bod"},
		{"empty strip argument leaves text", "s = s.strip('') + '!'", " old \n", " old \n!"},
		{"ASCII upper", "s = s.upper()", "mixed Case\n", "MIXED CASE\n"},
		{"ASCII lower", "s = s.lower()", "Mixed CASE\n", "mixed case\n"},
		{"bounded repetition both operand orders", "s = 2 * 'é' + '🙂' * 3", "old\n", "éé🙂🙂🙂"},
		{"nonpositive repetition", "s = 'x' * -2 + 'y' * 0 + 'empty'", "old\n", "empty"},
		{"empty search all unicode boundaries", "s = s.replace('', '|')", "é🙂", "|é|🙂|"},
		{"empty search count", "s = s.replace('', '|', 2)", "é🙂", "|é|🙂"},
		{"empty search zero count", "s = s.replace('', '|', 0) + '!'", "é🙂", "é🙂!"},
		{"empty buffer empty search", "s = s.replace('', 'new')", "", "new"},
		{"join literal list", "s = '-'.join(['é', '', '🙂'])", "old\n", "é--🙂"},
		{"join named tuple", "parts = ('é', '🙂')\ns = '|'.join(parts)", "old\n", "é|🙂"},
		{"split whitespace", "s = '|'.join(s.split())", " \té  🙂\n x \n", "é|🙂|x"},
		{"split literal delimiter preserves empties", "s = '|'.join(s.split(','))", ",é,,🙂,", "|é||🙂|"},
		{"splitlines python boundaries", "s = '|'.join(s.splitlines())", "é\r\n🙂\rX\vY\fZ\u0085Q\u2028R\u2029", "é|🙂|X|Y|Z|Q|R"},
		{"splitlines no phantom terminal line", "s = '|'.join(s.splitlines())", "a\n\nb\n", "a||b"},
		{"empty splitlines join", "s = 'new' + '|'.join(s.splitlines())", "", "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runPythonExpansion(t, "s = open('target.txt').read()\n"+tc.body+"\nopen('target.txt', 'w').write(s)\n", map[string]string{"target.txt": tc.before}, map[string]string{"target.txt": tc.after})
		})
	}
}

func TestLiveDiffPythonExpansionHelperSequencesAndDictionaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, script string
		after        map[string]string
	}{
		{"positional literal list", "def edit(names):\n    for name in names:\n        open(name, 'w').write('new')\nedit(['a.txt', 'b.txt'])\n", map[string]string{"a.txt": "new", "b.txt": "new"}},
		{"keyword caller bound tuple", "names = ('a.txt', 'b.txt')\ndef edit(paths):\n    for path in paths:\n        open(path, 'w').write('new')\nedit(paths=names)\n", map[string]string{"a.txt": "new", "b.txt": "new"}},
		{"default replacement pairs", "def edit(pairs=[('old', 'middle'), ('middle', 'new')]):\n    s = open('target.txt').read()\n    for old, new in pairs:\n        s = s.replace(old, new)\n    open('target.txt', 'w').write(s)\nedit()\n", map[string]string{"target.txt": "new\n"}},
		{"named pairs scope isolation", "pairs = [('old', 'global')]\ndef edit(pairs):\n    s = open('target.txt').read()\n    for old, new in pairs:\n        s = s.replace(old, new)\n    pairs = [('old', 'local')]\n    open('target.txt', 'w').write(s)\nedit(pairs=(('old', 'argument'),))\nfor old, new in pairs:\n    open('global.txt', 'w').write(new)\n", map[string]string{"target.txt": "argument\n", "global.txt": "global"}},
		{"join helper default sequence", "def edit(parts=('é', '🙂')):\n    open('target.txt', 'w').write('-'.join(parts))\nedit()\n", map[string]string{"target.txt": "é-🙂"}},
		{"dictionary keys retain insertion order", "s = ''\nfor key in {'b': 'first', 'a': 'second', 'b': 'last'}:\n    s += key\nopen('target.txt', 'w').write(s)\n", map[string]string{"target.txt": "ba"}},
		{"dictionary items duplicate key last value original position", "s = ''\nfor key, value in {'b': 'first', 'a': 'second', 'b': 'last'}.items():\n    s += key + ':' + value + ';'\nopen('target.txt', 'w').write(s)\n", map[string]string{"target.txt": "b:last;a:second;"}},
		{"named dictionary replacement pairs", "pairs = {'old': 'middle', 'middle': 'new'}\ns = open('target.txt').read()\nfor old, new in pairs.items():\n    s = s.replace(old, new)\nopen('target.txt', 'w').write(s)\n", map[string]string{"target.txt": "new\n"}},
		{"helper named dictionary argument and isolation", "pairs = {'old': 'global'}\ndef edit(pairs):\n    s = open('target.txt').read()\n    for old, new in pairs.items():\n        s = s.replace(old, new)\n    pairs = {'old': 'local'}\n    open('target.txt', 'w').write(s)\nedit({'old': 'argument'})\nfor old, new in pairs.items():\n    open('global.txt', 'w').write(new)\n", map[string]string{"target.txt": "argument\n", "global.txt": "global"}},
		{"dictionary default and keyword", "def edit(pairs={'old': 'default'}):\n    s = open('target.txt').read()\n    for old, new in pairs.items():\n        s = s.replace(old, new)\n    open('target.txt', 'w').write(s)\nedit()\nedit(pairs={'old': 'keyword'})\n", map[string]string{"target.txt": "default\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runPythonExpansion(t, tc.script, map[string]string{"target.txt": "old\n"}, tc.after)
		})
	}
}

func TestLiveDiffPythonExpansionRejectsUnsafeOrUnknownExpressions(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"non-ASCII uppercase fails closed":      "s = 'straße'.upper()",
		"non-ASCII lowercase fails closed":      "s = 'ÉABC'.lower()",
		"unknown replacement search":            "s = s.replace(unknown, 'new')",
		"unknown replacement value":             "s = s.replace('old', unknown)",
		"arbitrary evaluation":                  "s = eval(\"'new'\")",
		"regex replacement":                     "import re\ns = re.sub('old', 'new', s)",
		"filesystem iteration":                  "from pathlib import Path\nfor p in Path('.').iterdir():\n    s += 'new'",
		"host effect":                           "import os\nos.system('touch marker.txt')\ns = 'new'",
		"dynamic dictionary key":                "key = 'old'\npairs = {key: 'new'}\nfor old, new in pairs.items():\n    s = s.replace(old, new)",
		"dynamic dictionary value":              "value = 'new'\npairs = {'old': value}\nfor old, new in pairs.items():\n    s = s.replace(old, new)",
		"dictionary mutation":                   "pairs = {'old': 'new'}\npairs['old'] = 'wrong'\nfor old, new in pairs.items():\n    s = s.replace(old, new)",
		"dictionary method mutation":            "pairs = {'old': 'new'}\npairs.update({'old': 'wrong'})\nfor old, new in pairs.items():\n    s = s.replace(old, new)",
		"sequence helper local must not leak":   "def edit():\n    names = ['new']\nedit()\ns = ''.join(names)",
		"dictionary helper local must not leak": "def edit():\n    pairs = {'old': 'new'}\nedit()\nfor old, new in pairs.items():\n    s = s.replace(old, new)",
		"unknown join element":                  "s = ''.join(['new', unknown])",
		"invalid strip arity":                   "s = s.strip('o', 'd')",
		"invalid split empty separator":         "s = ''.join(s.split(''))",
		"out of range unicode index":            "s = s[100]",
		"oversize repetition":                   fmt.Sprintf("s = 'x' * %d", liveDiffPreviewFileLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runPythonExpansion(t, "s = open('target.txt').read()\n"+body+"\nopen('target.txt', 'w').write(s)\n", map[string]string{"target.txt": "old\n"}, nil)
		})
	}
}

func TestLiveDiffPythonExpansionArrivingExpressions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body, completion, after string }{
		{"concatenated replacement", "s = s.replace('o' + 'ld', 'head\\n' + '''arriving\nnext\n", "''')\n", "head\narriving\nnext\n\n"},
		{"source derived search and chained replacement", "s = s.replace(s[:3], 'middle').replace('mid' + 'dle', '''arriving\nnext\n", "''')\n", "arriving\nnext\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			before := map[string]string{"target.txt": "old\n"}
			writeTestFile(t, filepath.Join(directory, "target.txt"), before["target.txt"])
			worker := liveDiffPreviewWorker{ctx: t.Context()}
			prefix := "python3 - <<'PY'\ns = open('target.txt').read()\n" + tc.body
			files, recognized, err := worker.projectShell(prefix, directory, false)
			if err != nil || !recognized {
				t.Fatalf("arriving expression recognized=%v err=%v files=%+v", recognized, err, files)
			}
			// Only the arriving replacement has reached the preview, not the
			// original suffix newline beyond its tip.
			assertPythonExpansionDiffs(t, directory, files, before, map[string]string{"target.txt": strings.TrimSuffix(tc.after, "\n")})
			files, recognized, err = worker.projectShell(prefix+tc.completion+"open('target.txt', 'w').write(s)\nPY\n", directory, true)
			if err != nil || !recognized {
				t.Fatalf("completed expression recognized=%v err=%v files=%+v", recognized, err, files)
			}
			assertPythonExpansionDiffs(t, directory, files, before, map[string]string{"target.txt": tc.after})
		})
	}
	t.Run("unfinished search must not predict", func(t *testing.T) {
		directory := t.TempDir()
		before := map[string]string{"target.txt": "old\n"}
		writeTestFile(t, filepath.Join(directory, "target.txt"), before["target.txt"])
		worker := liveDiffPreviewWorker{ctx: t.Context()}
		files, _, err := worker.projectShell("python3 - <<'PY'\ns = open('target.txt').read()\ns = s.replace('o' + '''ld\n", directory, false)
		if err != nil {
			t.Fatal(err)
		}
		assertPythonExpansionDiffs(t, directory, files, before, nil)
	})
}

func TestLiveDiffPythonExpansionRetainsIterationBounds(t *testing.T) {
	t.Parallel()
	for _, n := range []int{256, 257} {
		for _, kind := range []string{"helper sequence", "dictionary items", "split sequence"} {
			t.Run(fmt.Sprintf("%s/%d", kind, n), func(t *testing.T) {
				var script string
				switch kind {
				case "helper sequence":
					script = "def edit(parts):\n    s = ''\n    for part in parts:\n        s += part\n    open('target.txt', 'w').write(s)\nedit([" + strings.Repeat("'x',", n) + "])\n"
				case "dictionary items":
					var pairs strings.Builder
					for i := range n {
						fmt.Fprintf(&pairs, "'k%d': 'x',", i)
					}
					script = "pairs = {" + pairs.String() + "}\ns = ''\nfor key, value in pairs.items():\n    s += value\nopen('target.txt', 'w').write(s)\n"
				case "split sequence":
					script = "s = ''\nfor part in '" + strings.TrimSuffix(strings.Repeat("x,", n), ",") + "'.split(','):\n    s += part\nopen('target.txt', 'w').write(s)\n"
				}
				var after map[string]string
				if n == 256 {
					after = map[string]string{"target.txt": strings.Repeat("x", n)}
				}
				runPythonExpansion(t, script, map[string]string{"target.txt": "old\n"}, after)
			})
		}
	}
}

func runPythonExpansion(t *testing.T, script string, before, after map[string]string) {
	t.Helper()
	directory := t.TempDir()
	for name, text := range before {
		writeTestFile(t, filepath.Join(directory, name), text)
	}
	files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, "python3 - <<'PY'\n"+script+"PY\n"), directory, false)
	if err != nil || (after == nil && recognized) || (after != nil && !recognized) {
		t.Fatalf("prediction recognized=%v err=%v files=%+v", recognized, err, files)
	}
	assertPythonExpansionDiffs(t, directory, files, before, after)
}

func assertPythonExpansionDiffs(t *testing.T, directory string, files []mekugi.ReviewFile, before, after map[string]string) {
	t.Helper()
	changed := make(map[string]string)
	for name, text := range after {
		if original, exists := before[name]; !exists || original != text {
			changed[name] = text
		}
	}
	if len(files) != len(changed) {
		t.Fatalf("predicted files=%+v, want %d changed targets", files, len(changed))
	}
	display := func(text string) string {
		text = strings.ReplaceAll(text, "\r", "")
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return text
	}
	for _, file := range files {
		name, err := filepath.Rel(directory, file.AfterPath)
		if err != nil {
			t.Fatal(err)
		}
		text, exists := changed[name]
		if !exists {
			t.Fatalf("unexpected or duplicate target %q: %+v", name, files)
		}
		delete(changed, name)
		beforePath := ""
		if _, exists := before[name]; exists {
			beforePath = filepath.Join(directory, name)
		}
		afterPath := filepath.Join(directory, name)
		want := mekugi.RenderReviewFile(beforePath, afterPath, display(before[name]), display(text))
		if file.BeforePath != beforePath || file.AfterPath != afterPath || file.Diff != want.Diff {
			t.Fatalf("target %s: got paths %q -> %q diff:\n%s\nwant paths %q -> %q diff:\n%s", name, file.BeforePath, file.AfterPath, file.Diff, beforePath, afterPath, want.Diff)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != len(before) {
		t.Fatalf("preview changed directory entries: entries=%v err=%v", entries, err)
	}
	for name, original := range before {
		got, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(got) != original {
			t.Fatalf("preview mutated %s: got=%q err=%v, want %q", name, got, err, original)
		}
	}
}
