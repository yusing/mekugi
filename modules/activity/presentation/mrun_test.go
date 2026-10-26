package presentation

import (
	"strings"
	"testing"
)

func TestMRunSourceUsesExecutionGrammar(t *testing.T) {
	for _, source := range []string{
		"mrun --tail -n 35 git commit -m 'fix: x'",
		"mrun --max-tokens=15501 git commit -m x",
		"mrun --max-tokens=4000 -- git commit -m 'fix: x'",
		"env -u BASH_ENV mrun -n 35 timeout 10 git commit -m 'fix: x'",
		"mrun -n 35 mrun -n 4 git commit -m 'fix: x'",
	} {
		if got := ShellSource(source, ""); !strings.HasPrefix(got, "Commit ") {
			t.Errorf("%q classified as %q", source, got)
		}
		if !VCSOutputSource(source) || !VCSPreservesDirectory(source) {
			t.Errorf("wrapped output/directory source not recognized: %q", source)
		}
	}
	for _, source := range []string{
		"mrun --tail git commit -m x",
		"mrun -n 0 git commit -m x",
		"mrun -n 01 git commit -m x",
		"mrun -n 1 -n 2 git commit -m x",
		"mrun --tail --tail -n 2 git commit -m x",
		"mrun -n $LIMIT git commit -m x",
		"mrun -n 2 $COMMAND commit -m x",
		"mrun --unknown -n 2 git commit -m x",
		"mrun -n 2 git commit -m x > result",
	} {
		if got := ShellSource(source, ""); !strings.HasPrefix(got, "Run") || VCSOutputSource(source) {
			t.Errorf("invalid/opaque source %q classified as %q", source, got)
		}
	}
	if got := ShellSource("mrun -n 2 cat source.go", "/workspace"); !strings.HasPrefix(got, "Read ") {
		t.Fatalf("wrapped read = %q", got)
	}
	for _, source := range []string{
		"mrun -n 35 cat source.go | head -n 5",
		"mrun -n 35 mrun -n 4 cat source.go | mrun -n 3 sed -n '1,2p'",
	} {
		if got := ShellSource(source, "/workspace"); !strings.HasPrefix(got, "Read ") {
			t.Errorf("wrapped pipeline %q = %q", source, got)
		}
	}
}

func TestMRunCommitEnvironmentKeepsOutputWithoutWrongRepository(t *testing.T) {
	for _, source := range []string{
		"env GIT_DIR=/other/.git mrun -n 35 git commit -m x",
		"mrun -n 35 env GIT_WORK_TREE=/other git -C /other commit -m x",
	} {
		rows, key := VCSOutputRows(source, "/workspace", "[main 3a1f9c2] x\n 1 file changed, 2 insertions(+)\n")
		if len(rows) != 1 || !rows[0].Footer || rows[0].Label != "3a1f9c2" || key != (CommitKey{}) {
			t.Errorf("%s: output=%+v key=%+v", source, rows, key)
		}
	}
}
