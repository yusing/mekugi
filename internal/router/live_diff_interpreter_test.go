package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestLiveDiffInterpreterWriteLiteralWrites(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command, path, before, after string
	}{
		{
			name:    "Python Path.write_text",
			command: "python3 - <<'PY'\nfrom pathlib import Path\nPath(\"new.txt\").write_text(\"new content\\n\")\nPY\n",
			path:    "new.txt",
			after:   "new content\n",
		},
		{
			name:    "Python open write",
			command: "python3 - <<'PY'\nopen(\"existing.txt\", \"w\").write(\"replacement\\n\")\nPY\n",
			path:    "existing.txt",
			before:  "old content\n",
			after:   "replacement\n",
		},
		{
			name:    "Python read_text replace once",
			command: "python3 - <<'PY'\nfrom pathlib import Path\npath = Path(\"existing.txt\")\npath.write_text(path.read_text().replace(\"old\", \"new\", 1))\nPY\n",
			path:    "existing.txt",
			before:  "old old\n",
			after:   "new old\n",
		},
		{
			name:    "JavaScript fs.writeFileSync",
			command: "node - <<'JS'\nconst fs = require(\"node:fs\");\nfs.writeFileSync(\"new.txt\", \"new content\\n\");\nJS\n",
			path:    "new.txt",
			after:   "new content\n",
		},
		{
			name:    "JavaScript fs.writeFileSync assigned path",
			command: "node - <<'JS'\nconst fs = require(\"node:fs\");\nconst target = \"new.txt\";\nfs.writeFileSync(target, \"new content\\n\");\nJS\n",
			path:    "new.txt",
			after:   "new content\n",
		},
		{
			name:    "JavaScript promises writeFile",
			command: "node - <<'JS'\nconst fs = require(\"node:fs/promises\");\nawait fs.writeFile(\"existing.txt\", \"replacement\\n\");\nJS\n",
			path:    "existing.txt",
			before:  "old content\n",
			after:   "replacement\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, tc.path)
			if tc.before != "" {
				if err := os.WriteFile(path, []byte(tc.before), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, tc.command), directory, false)
			if err != nil || !recognized || len(files) != 1 {
				t.Fatalf("prediction = %+v, %t, %v; want one literal-write diff", files, recognized, err)
			}
			file := files[0]
			beforePath := path
			if tc.before == "" {
				beforePath = ""
			}
			if file.BeforePath != beforePath || file.AfterPath != path {
				t.Fatalf("predicted paths = %q -> %q, want %q -> %q", file.BeforePath, file.AfterPath, beforePath, path)
			}
			if !strings.Contains(file.Diff, "+"+strings.TrimSuffix(tc.after, "\n")) {
				t.Errorf("diff %q does not add %q", file.Diff, tc.after)
			}
			if tc.before != "" && !strings.Contains(file.Diff, "-"+strings.TrimSuffix(tc.before, "\n")) {
				t.Errorf("diff %q does not remove original content %q", file.Diff, tc.before)
			}

			got, readErr := os.ReadFile(path)
			if tc.before == "" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("prediction created %q: read %q, %v", path, got, readErr)
				}
			} else if readErr != nil || string(got) != tc.before {
				t.Fatalf("prediction changed %q: got %q, %v; want original %q", path, got, readErr, tc.before)
			}
			entries, readDirErr := os.ReadDir(directory)
			wantEntries := 0
			if tc.before != "" {
				wantEntries = 1
			}
			if readDirErr != nil || len(entries) != wantEntries {
				t.Fatalf("prediction caused filesystem effects: entries=%v err=%v", entries, readDirErr)
			}
		})
	}
}

func TestLiveDiffInterpreterWriteDynamicBodiesFallBack(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command, path string
	}{
		{
			name:    "Python dynamic body",
			command: "python3 - <<'PY'\nimport os\nfrom pathlib import Path\nPath(\"target.txt\").write_text(os.environ[\"LIVE_DIFF_BODY\"])\nPY\n",
			path:    "target.txt",
		},
		{
			name:    "JavaScript dynamic body",
			command: "node - <<'JS'\nconst fs = require(\"node:fs\");\nfs.writeFileSync(\"target.txt\", process.env.LIVE_DIFF_BODY);\nJS\n",
			path:    "target.txt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, tc.path)
			const original = "existing content\n"
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}

			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, tc.command), directory, false)
			if err != nil || recognized || len(files) != 0 {
				t.Fatalf("dynamic body prediction = %+v, %t, %v; want fallback without a guessed diff", files, recognized, err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != original {
				t.Fatalf("dynamic prediction changed target: got %q, %v", got, readErr)
			}
		})
	}
}

func TestLiveDiffInterpreterWriteSkipsConditionalAndFunctionBodies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command string
	}{
		{
			name:    "Python conditional and function",
			command: "python3 - <<'PY'\nfrom pathlib import Path\nif True:\n    Path(\"conditional.txt\").write_text(\"conditional\\n\")\ndef update():\n    Path(\"function.txt\").write_text(\"function\\n\")\nPY\n",
		},
		{
			name:    "JavaScript conditional and function",
			command: "node - <<'JS'\nconst fs = require(\"node:fs\");\nif (true) { fs.writeFileSync(\"conditional.txt\", \"conditional\\n\"); }\nfunction update() { fs.writeFileSync(\"function.txt\", \"function\\n\"); }\nJS\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, tc.command), directory, false)
			if err != nil || recognized || len(files) != 0 {
				t.Fatalf("conditional/function prediction = %+v, %t, %v; want no predicted files", files, recognized, err)
			}
			entries, readDirErr := os.ReadDir(directory)
			if readDirErr != nil || len(entries) != 0 {
				t.Fatalf("conditional/function preview caused filesystem effects: entries=%v err=%v", entries, readDirErr)
			}
		})
	}
}

func TestLiveDiffInterpreterWriteCommonAgentEditForms(t *testing.T) {
	t.Parallel()
	const before = "package demo\n\nfunc old() {}\n"
	for _, tc := range []struct{ name, script string }{
		{"with open handles and encoding", "with open('demo.go', encoding='utf-8') as f:\n    s = f.read()\ns = s.replace('old', 'renamed', 1)\nwith open('demo.go', 'w', encoding='utf-8') as f:\n    f.write(s)\n"},
		{"Path text with encoding", "from pathlib import Path\np = Path('demo.go')\ntext = p.read_text(encoding='utf-8')\np.write_text(text.replace('old', 'renamed'), encoding='utf-8')\n"},
		{"named replacement strings and guards", "import sys\nfrom pathlib import Path\np = Path('demo.go')\nold = 'func old() {}'\nnew = 'func renamed() {}'\ntext = p.read_text()\nif old not in text:\n    raise SystemExit(f'missing {old!r}')\nelif text.count(old) != 1:\n    sys.exit(1)\nassert old in text\ntext = text.replace(old, new)\np.write_text(text)\nprint('updated', len(text))\n"},
		{"Path.open handle", "from pathlib import Path\np = Path('demo.go')\nwith p.open() as f:\n    s = f.read()\nwith p.open('w') as f:\n    f.write(s.replace('old', 'renamed'))\n"},
		{"helper mutating a global buffer", "s = open('demo.go').read()\ndef rep(a, b, count=1):\n    global s\n    assert s.count(a) == count, (a, s.count(a))\n    s = s.replace(a, b)\nrep('func old', 'func renamed')\nopen('demo.go', 'w').write(s)\n"},
		{"helper writes compose", "def edit(p, old, new):\n    s = open(p).read(); assert old in s, (p, old); open(p, 'w').write(s.replace(old, new, 1))\nedit('demo.go', 'func old', 'func tmp')\nedit(p='demo.go', old='func tmp', new='func renamed')\n"},
		{"slice between markers", "s = open('demo.go').read()\nold = s[s.index('func old'):s.index('{}')]\ns = s.replace(old, 'func renamed() ')\nopen('demo.go', 'w').write(s)\n"},
		{"splice by offsets", "s = open('demo.go').read()\na = s.index('func old'); b = a + len('func old')\ns = s[:a] + 'func renamed' + s[b:]\nopen('demo.go', 'w').write(s)\n"},
		{"reassigned path variable", "p = 'other.go'\np = 'demo.go'\ns = open(p).read()\nopen(p, 'w').write(s.replace('old', 'renamed'))\n"},
		{"cd before the script", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "demo.go")
			writeTestFile(t, path, before)
			var files []mekugi.ReviewFile
			var recognized bool
			var err error
			if tc.script == "" {
				// Literal `cd` steps select the directory of later steps.
				path = filepath.Join(directory, "sub", "demo.go")
				writeTestFile(t, path, before)
				w := liveDiffPreviewWorker{ctx: t.Context()}
				files, recognized, err = w.projectShell("set -e\nX=1\ncd sub && python3 - <<'PY'\ns = open('demo.go').read()\nopen('demo.go', 'w').write(s.replace('old', 'renamed'))\nPY\n", directory, true)
			} else {
				files, recognized, err = liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, "python3 - <<'PY'\n"+tc.script+"PY\n"), directory, false)
			}
			if err != nil || !recognized || len(files) != 1 || files[0].AfterPath != path ||
				!strings.Contains(files[0].Diff, "-func old() {}") || !strings.Contains(files[0].Diff, "+func renamed() {}") {
				t.Fatalf("prediction = %+v, %t, %v", files, recognized, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != before {
				t.Fatalf("prediction changed target: %q, %v", got, err)
			}
		})
	}
}

func TestLiveDiffInterpreterWriteRejectsEffectfulGuardsAndHandles(t *testing.T) {
	t.Parallel()
	for _, script := range []string{
		"from pathlib import Path\np = Path('demo.go')\ns = p.read_text()\nif 'old' in s:\n    s = s.replace('old', 'new')\np.write_text(s)\n",
		"from pathlib import Path\np = Path('demo.go')\ns = p.read_text()\nif 'old' not in s:\n    cleanup()\np.write_text(s.replace('old', 'new'))\n",
		"with open('demo.go', 'a') as f:\n    f.write('tail')\n",
		"with lock():\n    open('demo.go', 'w').write('new')\n",
	} {
		directory := t.TempDir()
		writeTestFile(t, filepath.Join(directory, "demo.go"), "old\n")
		files, _, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, "python3 - <<'PY'\n"+script+"PY\n"), directory, false)
		if err != nil || len(files) != 0 {
			t.Fatalf("unsupported effect predicted for %q: %+v, %v", script, files, err)
		}
	}
}

// Generated hunks must project back to exactly the predicted content, even
// where their old lines also occur earlier in the file.
func TestLiveDiffPredictedPatchProjectsPredictedContent(t *testing.T) {
	t.Parallel()
	repeated := strings.Repeat("x := 1\ny := 2\nz := 3\nw := 4\n", 3)
	for _, tc := range []struct{ name, before, after string }{
		{"repeated block edits the last copy", repeated, repeated[:len(repeated)/3*2] + "x := 1\ny := 20\nz := 3\nw := 4\n"},
		{"insertion at the start", "a\nb\nc\nd\ne\n", "new\na\nb\nc\nd\ne\n"},
		{"insertion at the end", "a\nb\nc\nd\ne\n", "a\nb\nc\nd\ne\nnew\n"},
		{"several distant hunks", strings.Repeat("same\n", 20) + "tail\n", "head\n" + strings.Repeat("same\n", 10) + "middle\n" + strings.Repeat("same\n", 10) + "tail\n"},
		{"everything removed", "a\nb\n", ""},
		{"no final newline", "a\nb\nc", "a\nB\nc"},
		{"carriage returns", "a\r\nb\r\nc\r\n", "a\r\nB\r\nc\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "file.go")
			writeTestFile(t, path, tc.before)
			patch, baseline, err := liveDiffPredictedPatch(t.Context(), []liveDiffPredictedWrite{{path: path, content: tc.after, tip: len(tc.after)}})
			if err != nil {
				t.Fatal(err)
			}
			files, err := stockPatchReviewPreview(baseline, "", patch, true)
			if err != nil || len(files) != 1 {
				t.Fatalf("projection of\n%s= %+v, %v", patch, files, err)
			}
			// Display drops carriage returns and missing final newlines.
			display := func(text string) string {
				text = strings.ReplaceAll(text, "\r", "")
				if text != "" && !strings.HasSuffix(text, "\n") {
					text += "\n"
				}
				return text
			}
			if want := mekugi.RenderReviewFile(path, path, display(tc.before), display(tc.after)); files[0].Diff != want.Diff {
				t.Fatalf("patch\n%s\nprojected\n%s\nwant\n%s", patch, files[0].Diff, want.Diff)
			}
		})
	}
}

func TestLiveDiffPredictedPatchStopsAtArrivingTip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "demo.go")
	before := "package demo\n\nfunc keep() {}\n\nfunc old() {\n\treturn\n}\n\nfunc tail() {}\n"
	writeTestFile(t, path, before)
	old := "func old() {\n\treturn\n}\n"
	next := "func renamed() {\n\tprepare()\n"
	after := strings.Replace(before, old, next, 1)
	tip := strings.Index(before, old) + len(next)
	patch, baseline, err := liveDiffPredictedPatch(t.Context(), []liveDiffPredictedWrite{{path: path, content: after, tip: tip, arriving: true}})
	if err != nil || strings.Contains(patch, "*** End Patch") || strings.Contains(patch, "tail") {
		t.Fatalf("arriving patch = %q, %v", patch, err)
	}
	files, err := stockPatchReviewPreview(baseline, "", patch, false)
	if err != nil || len(files) != 1 {
		t.Fatalf("arriving projection = %+v, %v", files, err)
	}
	diff := files[0].Diff
	removed, added := strings.Index(diff, "-func old() {"), strings.Index(diff, "+func renamed() {")
	if removed < 0 || added < removed || !strings.Contains(diff, "+\tprepare()") || strings.Contains(diff, "-func tail") {
		t.Fatalf("arriving region is not removed-then-added up to its tip:\n%s", diff)
	}
}

func TestLiveDiffInterpreterHelperStreamsArrivingReplacement(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "demo.go")
	writeTestFile(t, path, "package demo\n\nfunc old() {}\n\nfunc tail() {}\n")
	w := liveDiffPreviewWorker{ctx: t.Context()}
	prefix := "python3 - <<'PY'\ns = open('demo.go').read()\ndef rep(a, b):\n    global s\n    s = s.replace(a, b)\nrep('func old() {}', '''func renamed() {\n\tprepare()\n"
	files, recognized, err := w.projectShell(prefix, directory, false)
	if err != nil || !recognized || len(files) != 1 || files[0].AfterPath != path {
		t.Fatalf("arriving helper replacement = %+v, %t, %v", files, recognized, err)
	}
	if diff := files[0].Diff; !strings.Contains(diff, "-func old() {}") || !strings.Contains(diff, "+\tprepare()") || strings.Contains(diff, "-func tail") {
		t.Fatalf("arriving helper replacement diff:\n%s", diff)
	}
}

func TestLiveDiffInterpreterArrivingHelperKeepsEveryWrite(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"complete then arriving": "    open('a.txt', 'w').write('one\\nfoo')\n    open('b.txt', 'w').write(a)\n",
		"arriving twice":         "    open('a.txt', 'w').write(a)\n    open('b.txt', 'w').write(a)\n",
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "a.txt"), "a b\n")
			writeTestFile(t, filepath.Join(directory, "b.txt"), "a b\n")
			w := liveDiffPreviewWorker{ctx: t.Context()}
			files, recognized, err := w.projectShell("python3 - <<'PY'\ndef f(a):\n"+body+"f('''line1\nline2\n", directory, false)
			if err != nil || !recognized || len(files) != 2 {
				t.Fatalf("arriving helper writes = %+v, %t, %v", files, recognized, err)
			}
			for _, file := range files {
				if !strings.Contains(file.Diff, "-a b") {
					t.Fatalf("%s lost its replaced line:\n%s", file.AfterPath, file.Diff)
				}
			}
		})
	}
}

func TestLiveDiffInterpreterHelperLocalsDoNotLeak(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "demo.go"), "old\n")
	// The helper's local s is not the module's s, which stays unresolved.
	script := "def edit(p):\n    s = open(p).read()\nedit('demo.go')\nopen('demo.go', 'w').write(s.replace('old', 'new'))\n"
	files, _, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, "python3 - <<'PY'\n"+script+"PY\n"), directory, false)
	if err != nil || len(files) != 0 {
		t.Fatalf("helper local leaked into the module: %+v, %v", files, err)
	}
}

func TestLiveDiffPredictedPatchRejectsUnrepresentablePaths(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "a\n*** Delete File: b")
	if _, _, err := liveDiffPredictedPatch(t.Context(), []liveDiffPredictedWrite{{path: path, content: "x\n", tip: 2}}); err == nil {
		t.Fatal("a path with a newline could forge patch operations")
	}
}

func TestLiveDiffPredictedPatchFinalNewlineOnlyChangeShowsNothing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file.go")
	writeTestFile(t, path, "a\nb")
	patch, _, err := liveDiffPredictedPatch(t.Context(), []liveDiffPredictedWrite{{path: path, content: "a\nb\n", tip: 4}})
	if err != nil || strings.Contains(patch, "*** Update File") {
		t.Fatalf("final-newline change = %q, %v", patch, err)
	}
}
