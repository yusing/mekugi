package execscope

import (
	"path/filepath"
	"testing"
	"time"
)

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
