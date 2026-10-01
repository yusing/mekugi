package router

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveDiffPythonCoverageLiteralLoops(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, script  string
		before, after map[string]string
	}{
		{
			name:   "path assignments inside list loop",
			script: "from pathlib import Path\nfor name in ['a.txt', 'b.txt']:\n    p = Path(name)\n    s = p.read_text()\n    p.write_text(s.replace('old', 'new'))\n",
			before: map[string]string{"a.txt": "old a\n", "b.txt": "old b\n"},
			after:  map[string]string{"a.txt": "new a\n", "b.txt": "new b\n"},
		},
		{
			name:   "tuple replacements compose in order",
			script: "s = open('target.txt').read()\nfor old, new in [('old', 'middle'), ('middle', 'new')]:\n    s = s.replace(old, new)\nopen('target.txt', 'w').write(s)\n",
			before: map[string]string{"target.txt": "old old\n"}, after: map[string]string{"target.txt": "new new\n"},
		},
		{
			name:   "list unpacking and named tuple",
			script: "pairs = (['a.txt', 'alpha'], ['b.txt', 'beta'])\nfor [name, text] in pairs:\n    open(name, 'w').write(text)\n",
			after:  map[string]string{"a.txt": "alpha", "b.txt": "beta"},
		},
		{
			name:   "named list and scalar tuple retain final binding",
			script: "names = ['a', 'b']\ns = ''\nfor name in names:\n    s += name\nfor suffix in ('c', 'd'):\n    s += suffix\nopen('target.txt', 'w').write(s + name + suffix)\n",
			after:  map[string]string{"target.txt": "abcdbd"},
		},
		{
			name:   "nested loops retain bindings",
			script: "s = ''\nfor a in ['a', 'b']:\n    for b in ('1', '2'):\n        s += a + b\nopen('target.txt', 'w').write(s + a + b)\n",
			after:  map[string]string{"target.txt": "a1a2b1b2b2"},
		},
		{
			name:   "repeated writes and reads use predicted content",
			script: "for old, new in [('old', 'middle'), ('middle', 'new')]:\n    s = open('target.txt').read()\n    s = s.replace(old, new)\n    open('target.txt', 'w').write(s)\n",
			before: map[string]string{"target.txt": "old\n"}, after: map[string]string{"target.txt": "new\n"},
		},
		{
			name:   "empty loop keeps earlier binding",
			script: "name = 'kept'\nfor name in []:\n    name = 'wrong'\nopen('target.txt', 'w').write(name)\n",
			after:  map[string]string{"target.txt": "kept"},
		},
		{
			name:   "helper loop local scope",
			script: "part = 'global'\ndef edit(path):\n    s = ''\n    for part in ['local', 'text']:\n        s += part\n    open(path, 'w').write(s + part)\nedit('local.txt')\nopen('global.txt', 'w').write(part)\n",
			after:  map[string]string{"local.txt": "localtexttext", "global.txt": "global"},
		},
		{
			name:   "loop invokes helper mutating global buffer",
			script: "s = open('target.txt').read()\ndef append(part):\n    global s\n    s += part\nfor part in ['a', 'b']:\n    append(part)\nopen('target.txt', 'w').write(s)\n",
			before: map[string]string{"target.txt": "old"}, after: map[string]string{"target.txt": "oldab"},
		},
		{
			name:   "helper appends global text and local argument",
			script: "suffix = 'global'\ns = open('target.txt').read()\ndef append(part):\n    global s\n    s += part + suffix\nappend('local')\nopen('target.txt', 'w').write(s)\n",
			before: map[string]string{"target.txt": "old"}, after: map[string]string{"target.txt": "oldlocalglobal"},
		},
		{
			name:   "Path division loop bindings",
			script: "from pathlib import Path\nfor name in ['a.txt', 'b.txt']:\n    p = Path('.') / name\n    p.write_text('new')\n",
			after:  map[string]string{"a.txt": "new", "b.txt": "new"},
		},
		{
			name:   "Path division loop bindings survive later reassignment",
			script: "from pathlib import Path\nfor name in ['a.txt', 'b.txt']:\n    p = Path('.') / name\n    p.write_text('new')\nname = 'c.txt'\n",
			after:  map[string]string{"a.txt": "new", "b.txt": "new"},
		},
		{
			name:   "Path division straight-line binding",
			script: "from pathlib import Path\nname = 'a.txt'\np = Path('.') / name\nname = 'b.txt'\np.write_text('new')\n",
			after:  map[string]string{"a.txt": "new"},
		},
		{
			name:   "Path division augmented string binding",
			script: "from pathlib import Path\nname = 'a'\nname += '.txt'\np = Path('.') / name\nname += '.other'\np.write_text('new')\n",
			after:  map[string]string{"a.txt": "new"},
		},
		{
			name:   "Path division helper arguments and scope restoration",
			script: "from pathlib import Path\nname = 'global.txt'\ndef edit(name):\n    p = Path('.') / name\n    name = 'unused.txt'\n    p.write_text('local')\nedit('local.txt')\n(Path('.') / name).write_text('global')\n",
			after:  map[string]string{"local.txt": "local", "global.txt": "global"},
		},
		{
			name:   "Path division helper local segment",
			script: "from pathlib import Path\nname = 'global.txt'\ndef edit():\n    name = 'local'\n    name += '.txt'\n    p = Path('.') / name\n    p.write_text('local')\nedit()\n(Path('.') / name).write_text('global')\n",
			after:  map[string]string{"local.txt": "local", "global.txt": "global"},
		},
		{
			name:   "helper augmented parameter binding",
			script: "s = 'global'\ndef helper(s):\n    s += 'suffix'\n    open('local.txt', 'w').write(s)\nhelper('local')\nopen('global.txt', 'w').write(s)\n",
			after:  map[string]string{"local.txt": "localsuffix", "global.txt": "global"},
		},
		{
			name:   "helper augmented explicit global binding",
			script: "s = 'global'\ndef helper():\n    global s\n    s += 'suffix'\n    open('target.txt', 'w').write(s)\nhelper()\n",
			after:  map[string]string{"target.txt": "globalsuffix"},
		},
		{
			name:   "augmented assignment known replacement RHS",
			script: "s = open('target.txt').read()\npart = 'old'\ns += part.replace('old', 'new')\ns += '!' + part\nopen('target.txt', 'w').write(s)\n",
			before: map[string]string{"target.txt": "old"}, after: map[string]string{"target.txt": "oldnew!old"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			for name, text := range tc.before {
				writeTestFile(t, filepath.Join(directory, name), text)
			}
			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, "python3 - <<'PY'\n"+tc.script+"PY\n"), directory, false)
			if err != nil || !recognized {
				t.Fatalf("prediction recognized=%v err=%v files=%+v", recognized, err, files)
			}
			assertPythonExpansionDiffs(t, directory, files, tc.before, tc.after)
		})
	}
}

func TestLiveDiffPythonCoverageRejectsUnsupportedLoopsAndMutations(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"dynamic range":                          "for x in range(2):\n    s += 'x'\n",
		"dynamic iterable":                       "for x in values:\n    s += x\n",
		"nonliteral element":                     "for x in ['known', unknown]:\n    s += x\n",
		"integer element":                        "for x in ['known', 1]:\n    s += 'x'\n",
		"async loop":                             "async for x in ['x']:\n    s += x\n",
		"loop else":                              "for x in ['x']:\n    s += x\nelse:\n    s += 'else'\n",
		"break":                                  "for x in ['x']:\n    s += x\n    break\n",
		"continue":                               "for x in ['x']:\n    continue\n    s += x\n",
		"effectful body":                         "for x in ['x']:\n    unknown_effect()\n    s += x\n",
		"short unpack":                           "for a, b in [('x',)]:\n    s += a\n",
		"long unpack":                            "for a, b in [('x', 'y', 'z')]:\n    s += a\n",
		"nested unpack":                          "for a, (b, c) in [('x', ('y', 'z'))]:\n    s += a\n",
		"starred unpack":                         "for a, *b in [('x', 'y')]:\n    s += a\n",
		"attribute target":                       "for obj.name in ['x']:\n    s += 'x'\n",
		"empty loop leaves target unbound":       "for x in ():\n    s += x\ns += x\n",
		"helper loop target stays local":         "def helper():\n    for x in ['x']:\n        local = x\nhelper()\ns += x\n",
		"helper augmented local before binding":  "def helper():\n    s += 'suffix'\n    open('target.txt', 'w').write(s)\nhelper()\n",
		"helper assignment local before binding": "def helper():\n    open('target.txt', 'w').write(s)\n    s = 'local'\nhelper()\n",
		"helper loop target before binding":      "def helper():\n    open('target.txt', 'w').write(s)\n    for s in ['local']:\n        local = s\nhelper()\n",
		"unknown augmented LHS":                  "unknown += 'x'\n",
		"unknown augmented RHS":                  "s += unknown\n",
		"non-string augmented LHS":               "n = 1\nn += 'x'\n",
		"non-string augmented RHS":               "s += 1\n",
		"subtract assignment":                    "s -= 'x'\n",
		"multiply assignment":                    "s *= 2\n",
		"attribute augmented assignment":         "obj.s += 'x'\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "target.txt"), "old\n")
			script := "s = open('target.txt').read()\ns = s.replace('old', 'new')\n" + body + "open('target.txt', 'w').write(s)\n"
			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, "python3 - <<'PY'\n"+script+"PY\n"), directory, false)
			if err != nil || recognized || len(files) != 0 {
				t.Fatalf("unsupported program produced prediction: files=%+v recognized=%v err=%v", files, recognized, err)
			}
			assertPythonExpansionDiffs(t, directory, files, map[string]string{"target.txt": "old\n"}, nil)
		})
	}
}

func TestLiveDiffPythonCoverageSharedIterationBound(t *testing.T) {
	t.Parallel()
	literal := func(n int) string { return "[" + strings.Repeat("'x',", n) + "]" }
	for _, tc := range []struct {
		name, loops string
		want        string
	}{
		{"exact bound", "for x in " + literal(256) + ":\n    s += x\n", strings.Repeat("x", 256)},
		{"over bound", "for x in " + literal(257) + ":\n    s += x\n", ""},
		{"nested exact bound", "for x in " + literal(16) + ":\n    for y in " + literal(15) + ":\n        s += y\n", strings.Repeat("x", 240)},
		{"nested shared bound exceeded", "for x in " + literal(16) + ":\n    for y in " + literal(16) + ":\n        s += y\n", ""},
		{"sequential shared bound exceeded", "for x in " + literal(128) + ":\n    s += x\nfor y in " + literal(129) + ":\n    s += y\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			script := "s = ''\n" + tc.loops + "open('target.txt', 'w').write(s)\n"
			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, "python3 - <<'PY'\n"+script+"PY\n"), directory, false)
			if err != nil || recognized != (tc.want != "") {
				t.Fatalf("bound prediction: files=%+v recognized=%v err=%v", files, recognized, err)
			}
			var after map[string]string
			if tc.want != "" {
				after = map[string]string{"target.txt": tc.want}
			}
			assertPythonExpansionDiffs(t, directory, files, nil, after)
		})
	}
}

func TestLiveDiffPythonCoverageArrivingAugmentedAssignment(t *testing.T) {
	t.Parallel()
	for name, script := range map[string]string{
		"read buffer source before final write": "s = open('target.txt').read()\ns += '''arriving\nnext\n",
		"helper global read buffer":             "s = open('target.txt').read()\ndef append(part):\n    global s\n    s += part\nappend('''arriving\nnext\n",
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			before := map[string]string{"target.txt": "old\n"}
			writeTestFile(t, filepath.Join(directory, "target.txt"), before["target.txt"])
			worker := liveDiffPreviewWorker{ctx: t.Context()}
			prefix := "python3 - <<'PY'\n" + script
			files, recognized, err := worker.projectShell(prefix, directory, false)
			if err != nil || !recognized || len(files) != 1 {
				t.Fatalf("arriving += prediction: files=%+v recognized=%v err=%v", files, recognized, err)
			}
			assertPythonExpansionDiffs(t, directory, files, before, map[string]string{"target.txt": "old\narriving\nnext\n"})
			completion := "'''\n"
			if strings.Contains(script, "append('''") {
				completion = "''')\n"
			}
			files, recognized, err = worker.projectShell(prefix+completion+"open('target.txt', 'w').write(s)\nPY\n", directory, true)
			if err != nil || !recognized {
				t.Fatalf("completed += prediction: files=%+v recognized=%v err=%v", files, recognized, err)
			}
			assertPythonExpansionDiffs(t, directory, files, before, map[string]string{"target.txt": "old\narriving\nnext\n"})
		})
	}
}
