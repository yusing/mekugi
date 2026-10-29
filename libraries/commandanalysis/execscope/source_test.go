package execscope

import (
	"slices"
	"strings"
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
		{"accumulated keys", "targets = {}\nfor name in ['a.txt', 'b.txt']:\n targets[Path(name)] = unknown()\nfor path, text in targets.items():\n path.write_text(text)", false, []string{"a.txt", "b.txt"}},
		{"accumulated values", "targets = {}\nfor name in ['a.txt', 'b.txt']:\n targets[name] = Path(name)\nfor name, path in targets.items():\n path.write_text('new')", false, []string{"a.txt", "b.txt"}},
		{"unknown accumulated key", "targets = {'a.txt': 1}\ntargets[unknown()] = 2\nfor path, text in targets.items():\n Path(path).write_text(text)", true, nil},
		{"unknown then known key", "targets = {unknown(): 1}\ntargets['a.txt'] = 2\nfor path, text in targets.items():\n Path(path).write_text(text)", true, nil},
		{"loop carried key", "targets = {}\nname = 'a.txt'\nfor unused in [1, 2]:\n targets[name] = 'after'\n name = 'b.txt'\nfor path, text in targets.items():\n Path(path).write_text(text)", true, nil},
		{"loop carried input", "targets = {}\nname = 'a.txt'\nfor unused in [1, 2]:\n path = Path(name)\n targets[path] = 'after'\n name = 'b.txt'\nfor path, text in targets.items():\n path.write_text(text)", true, nil},
		{"loop carried unpacking", "targets = {}\nname = 'a.txt'\nfor unused in [1, 2]:\n targets[name] = 'after'\n for name, value in {'b.txt': 1}.items():\n  pass\nfor path, text in targets.items():\n Path(path).write_text(text)", true, nil},
		{"loop carried assignment expression", "targets = {}\nname = 'a.txt'\nfor unused in [1, 2]:\n targets[name] = 'after'\n (name := 'b.txt')\nfor path, text in targets.items():\n Path(path).write_text(text)", true, nil},
		{"iterable evaluated before body", "targets = {'a.txt': 'after'}\nfor name, text in targets.items():\n targets = {}\n Path(name).write_text(text)", false, []string{"a.txt"}},
		{"candidate limit", "targets = {}\nfor name in [" + strings.Repeat("'a.txt',", ListingLimit+1) + "]:\n targets[Path(name)] = 'text'\nfor path, text in targets.items():\n path.write_text(text)", true, nil},
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
