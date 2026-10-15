package execscope

import (
	"slices"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

func TestPythonPathJoinUnknownSegmentsStayOpen(t *testing.T) {
	for _, source := range []string{"name = 'a'\nname += '.txt'\n(Path('src') / name).write_text('new')",
		"name = 'a.txt'\nname = unknown()\n(Path('src') / name).write_text('new')",
		"for name in ['a.txt', unknown()]:\n (Path('src') / name).write_text('new')",
	} {
		result := inspectExecSource(execProviderInput{cwd: t.TempDir(), deadline: time.Now().Add(time.Second)}, source, "", sourceparse.Python, true)
		if !result.open || len(result.scope) != 0 {
			t.Fatalf("unknown path segment reused a stale or partial binding: %+v", result)
		}
	}
}

func TestPythonDictionaryItemsScope(t *testing.T) {
	for _, tt := range []struct {
		name, source string
		open         bool
		want         []string
	}{
		{"inline", "for name, value in {'a.txt': unknown()}.items():\n (Path('out') / name).write_text('new')", false, []string{"out/a.txt"}},
		{"values", "targets = {'a': Path('out/a.txt'), 'b': Path('out/b.txt')}\nfor name, target in targets.items():\n target.write_text('new')", false, []string{"out/a.txt", "out/b.txt"}},
		{"shadow dictionary", "targets = {'a.txt': 'body'}\nfor targets, body in targets.items():\n (Path('out') / targets).write_text(body)", false, []string{"out/a.txt"}},
		{"absolute key", "targets = {'/tmp/absolute.txt': 1}\nfor name, value in targets.items():\n (Path('out') / name).write_text('new')", false, []string{"/tmp/absolute.txt"}},
		{"unknown key", "targets = {'a.txt': 1, unknown(): 2}\nfor name, value in targets.items():\n (Path('out') / name).write_text('new')", true, nil},
		{"dictionary unpack", "targets = {'a.txt': 1, **unknown()}\nfor name, value in targets.items():\n (Path('out') / name).write_text('new')", true, nil},
		{"reassigned", "targets = {'a.txt': 1}\ntargets = unknown()\nfor name, value in targets.items():\n (Path('out') / name).write_text('new')", true, nil},
		{"mutated", "targets = {'a.txt': 1}\ntargets.update(unknown())\nfor name, value in targets.items():\n (Path('out') / name).write_text('new')", true, nil},
		{"subscript", "targets = {'a.txt': 1}\ntargets[unknown()] = 2\nfor name, value in targets.items():\n (Path('out') / name).write_text('new')", true, nil},
		{"stale unpacked name", "name = 'old.txt'\nfor name, value in unknown():\n (Path('out') / name).write_text('new')", true, nil},
		{"alias", "targets = {'a.txt': 1}\nother = targets\nother[unknown()] = 2\nfor name, value in targets.items():\n (Path('out') / name).write_text('new')", true, nil},
		{"passed to function", "targets = {'a.txt': 1}\nmutate(targets)\nfor name, value in targets.items():\n (Path('out') / name).write_text('new')", true, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			result := inspectExecSource(execProviderInput{cwd: workspace, deadline: time.Now().Add(time.Second)}, tt.source, "", sourceparse.Python, true)
			var got []string
			for _, scope := range result.scope {
				for _, operand := range scope.Operands {
					got = append(got, operand.Path)
				}
			}
			var want []string
			for _, path := range tt.want {
				want = append(want, sourceparse.Path(workspace, path))
			}
			if result.open != tt.open || !slices.Equal(got, want) {
				t.Fatalf("scope = %v open=%v; want %v open=%v", got, result.open, want, tt.open)
			}
		})
	}
}
