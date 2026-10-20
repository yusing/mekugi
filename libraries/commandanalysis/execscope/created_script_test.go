package execscope

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCreatedScriptTargets(t *testing.T) {
	for _, test := range []struct {
		name, create, run, body string
		want                    bool
	}{
		{"python", "cat > edit.py", "python3 edit.py", "open('target', 'w').write('new')", true},
		{"node", "cat > edit.js", "node edit.js", "require('fs').writeFileSync('target', 'new')", true},
		{"different input", "cat > edit.py", "python3 other.py", "open('target', 'w').write('new')", false},
		{"append", "cat >> edit.py", "python3 edit.py", "open('target', 'w').write('new')", false},
		{"intervening command", "cat > edit.py", "rm edit.py; python3 edit.py", "open('target', 'w').write('new')", false},
		{"loader", "cat > edit.py", "python3 -m edit.py", "open('target', 'w').write('new')", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			plan := Classify(Input{Command: test.create + " <<'EOF'\n" + test.body + "\nEOF\n" + test.run, Directory: workspace, Shell: "bash", AuthoredOnly: true})
			found := slices.ContainsFunc(plan.Scope, func(entry Entry) bool {
				return slices.ContainsFunc(entry.Operands, func(operand Operand) bool { return operand.Path == filepath.Join(workspace, "target") })
			})
			if found != test.want {
				t.Fatalf("created script target present=%v, want %v: %+v", found, test.want, plan)
			}
		})
	}
}

func TestCreatedScriptKeepsExistingCandidates(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "edit.py"), []byte("open('old-target', 'w').write('old')"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := Classify(Input{Command: "cat > edit.py <<'PY'\nopen('new-target', 'w').write('new')\nPY\npython3 edit.py", Directory: workspace, Shell: "bash", AuthoredOnly: true})
	for _, target := range []string{"old-target", "new-target"} {
		if !slices.ContainsFunc(plan.Scope, func(entry Entry) bool {
			return slices.ContainsFunc(entry.Operands, func(operand Operand) bool { return operand.Path == filepath.Join(workspace, target) })
		}) {
			t.Fatalf("lost %s baseline candidate: %+v", target, plan)
		}
	}
}
