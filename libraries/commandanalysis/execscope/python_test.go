package execscope

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestPythonReplacementOperationProvenance(t *testing.T) {
	for _, tc := range []struct {
		source string
		write  bool
	}{
		{"def replace(old, new):\n    return old + new\nreplace('a', 'b')", false},
		{"def rename(old, new):\n    pass\nrename('a', 'b')", false},
		{`text = 'a'; text.replace('a', 'b')`, false},
		{`unknown.replace('a', 'b')`, false},
		{"from os import replace\ndef replace(old, new):\n    pass\nreplace('a', 'b')", false},
		{`import os; os = unknown; os.replace('a', 'b')`, false},
		{`from other import replace; replace('a', 'b')`, false},
		{`import os; os.replace('a', 'b')`, true},
		{`import os.path; os.replace('a', 'b')`, true},
		{"import os\ndef helper(os):\n    pass\nos.replace('a', 'b')", true},
		{"import os\ndef helper():\n    os = None\nos.replace('a', 'b')", true},
		{"from os import replace\ndef helper():\n    def replace(old, new):\n        pass\nreplace('a', 'b')", true},
		{"import os\nclass Helper:\n    os = None\nos.replace('a', 'b')", true},
		{`import os; helper = lambda os: None; os.replace('a', 'b')`, true},
		{`import os as files; files.rename('a', 'b')`, true},
		{`from os import replace as swap; swap('a', 'b')`, true},
		{`import shutil; shutil.move('a', 'b')`, true},
		{`import pathlib; pathlib.Path('a').replace('b')`, true},
		{`from pathlib import Path as P; p = P('a'); p.rename('b')`, true},
	} {
		t.Run(tc.source, func(t *testing.T) {
			directory := t.TempDir()
			result := execPythonScope(execProviderInput{identity: "python3", cwd: directory,
				args: []string{"-c", tc.source}, deadline: time.Now().Add(Budget), authoredOnly: true})
			var paths []string
			for _, entry := range result.scope {
				for _, operand := range entry.Operands {
					paths = append(paths, operand.Path)
				}
			}
			var want []string
			if tc.write {
				want = []string{filepath.Join(directory, "a"), filepath.Join(directory, "b")}
			}
			if result.open || !slices.Equal(paths, want) {
				t.Fatalf("replacement scope = %q, open=%v; want %q", paths, result.open, want)
			}
		})
	}
}

func TestPythonOpenModesPreserveWriteScope(t *testing.T) {
	for _, tc := range []struct {
		script string
		write  bool
	}{
		{`open("target.txt")`, false},
		{`open("target.txt", "rb")`, false},
		{`open("target.txt", encoding="utf-8")`, false},
		{`open("target.txt", mode="r", encoding="utf-8")`, false},
		{`from pathlib import Path; Path("target.txt").open(encoding="utf-8")`, false},
		{"with open('target.txt') as f:\n    s = f.read()\ns = s.replace('old', 'new')", false},
		{`open("target.txt", "w")`, true},
		{`open("target.txt", "a")`, true},
		{`open("target.txt", "x")`, true},
		{`open("target.txt", "r+")`, true},
		{`open("target.txt", encoding="utf-8", mode="w")`, true},
		{`from pathlib import Path; Path("target.txt").open(encoding="utf-8", mode="w")`, true},
	} {
		t.Run(tc.script, func(t *testing.T) {
			directory := t.TempDir()
			result := execPythonScope(execProviderInput{identity: "python3", cwd: directory,
				args: []string{"-c", tc.script}, deadline: time.Now().Add(Budget), authoredOnly: true})
			if result.open || (len(result.scope) != 0) != tc.write {
				t.Fatalf("mode lost read/write distinction: %+v", result)
			}
			if tc.write && (len(result.scope) != 1 || len(result.scope[0].Operands) != 1 || result.scope[0].Operands[0].Path != filepath.Join(directory, "target.txt")) {
				t.Fatalf("writer target coverage regressed: %+v", result)
			}
		})
	}
}
