package router

import (
	"reflect"
	"testing"
)

func TestCodeModeShellFragments(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []string
		candidate    bool
	}{
		{"partial batch", `const results = await Promise.allSettled([
tools.exec_command({cmd:"printf 'first'\nprintf 'first-tail'", note:"tools.exec_command({cmd:'fake'})"})`, []string{"printf 'first'\nprintf 'first-tail'"}, true},
		{"open Promise batch prefix", `const results = await Promise.allSettled([\n  `, nil, true},
		{"completed non-shell literal batch", `const results = await Promise.allSettled([42]); text(results)`, nil, false},
		{"nested object", `tools.exec_command({note:{cmd:"fake"}, "cmd":"cat real"})`, []string{"cat real"}, true},
		{"two calls and decoys", `// tools.exec_command({cmd:"fake"})
tools.exec_command({cmd:"cat first"}); const note="tools.exec_command({cmd:'fake'})";
tools.exec_command({cmd:"cat second"})`, []string{"cat first", "cat second"}, true},
		{"later dynamic value", `tools.exec_command({cmd:"cat old",cmd:variable})`, nil, true},
		{"concatenated value", `tools.exec_command({cmd:"cat old" + suffix})`, nil, true},
		{"later spread", `tools.exec_command({cmd:"cat old",...options})`, nil, true},
		{"computed override", `tools.exec_command({cmd:"cat old",["cmd"]:replacement})`, nil, true},
		{"unfinished computed override", `tools.exec_command({cmd:"cat old",["cmd"]:`, nil, true},
		{"method override", `tools.exec_command({cmd:"cat old",cmd(){return "new"}})`, nil, true},
		{"unfinished command ending in a brace", `tools.exec_command({cmd:"cat > f <<'EOF'\n}`, []string{"cat > f <<'EOF'\n}"}, true},
		{"unfinished later key", `tools.exec_command({cmd:"cat old", w`, []string{"cat old"}, true},
		{"unfinished key that may become cmd", `tools.exec_command({cmd:"cat old", c`, nil, true},
		{"unfinished accessor prefix", `tools.exec_command({cmd:"cat old", ge`, nil, true},
		{"non-shell batch", `const r=await Promise.allSettled(tasks);text(r)`, nil, false},
		{"only decoys", `const note="tools.exec_command({cmd:'fake'})"; // tools.exec_command({cmd:"also fake"})`, nil, false},
		{"regex decoy", `const matcher=/tools.exec_command\\({cmd:"fake"}\\)/;`, nil, false},
		{"qualified member decoy", `const fake={tools:{exec_command:x=>x}}; fake.tools.exec_command({cmd:"echo decoy"})`, nil, false},
		{"partial qualified member decoy", `fake./* gap */tools.exec_command({cmd:"echo decoy"`, nil, false},
		{"unicode identifier decoy", `const πtools={exec_command:x=>x}; πtools.exec_command({cmd:"echo decoy"})`, nil, false},
		{"unicode whitespace member decoy", "fake.\u00a0tools.exec_command({cmd:\"echo decoy\"", nil, false},
		{"BOM whitespace member decoy", "fake.\ufeff tools.exec_command({cmd:\"echo decoy\"", nil, false},
		{"other identifier start decoy", `const ℘tools={exec_command:x=>x}; ℘tools.exec_command({cmd:"echo decoy"})`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, candidate := codeModeShellFragments(tc.source)
			if candidate != tc.candidate || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("fragments = %#v, candidate %t; want %#v, candidate %t", got, candidate, tc.want, tc.candidate)
			}
		})
	}
}
