package router

import (
	"reflect"
	"testing"
)

func TestCodeModeShellFragments(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []string
	}{
		{"partial batch", `const results = await Promise.allSettled([
tools.exec_command({cmd:"printf 'first'\nprintf 'first-tail'", note:"tools.exec_command({cmd:'fake'})"})`, []string{"printf 'first'\nprintf 'first-tail'"}},
		{"nested object", `tools.exec_command({note:{cmd:"fake"}, "cmd":"cat real"})`, []string{"cat real"}},
		{"two calls and decoys", `// tools.exec_command({cmd:"fake"})
tools.exec_command({cmd:"cat first"}); const note="tools.exec_command({cmd:'fake'})";
tools.exec_command({cmd:"cat second"})`, []string{"cat first", "cat second"}},
		{"later dynamic value", `tools.exec_command({cmd:"cat old",cmd:variable})`, nil},
		{"concatenated value", `tools.exec_command({cmd:"cat old" + suffix})`, nil},
		{"later spread", `tools.exec_command({cmd:"cat old",...options})`, nil},
		{"computed override", `tools.exec_command({cmd:"cat old",["cmd"]:replacement})`, nil},
		{"unfinished computed override", `tools.exec_command({cmd:"cat old",["cmd"]:`, nil},
		{"method override", `tools.exec_command({cmd:"cat old",cmd(){return "new"}})`, nil},
		{"unfinished command ending in a brace", `tools.exec_command({cmd:"cat > f <<'EOF'\n}`, []string{"cat > f <<'EOF'\n}"}},
		{"unfinished later key", `tools.exec_command({cmd:"cat old", w`, []string{"cat old"}},
		{"unfinished key that may become cmd", `tools.exec_command({cmd:"cat old", c`, nil},
		{"unfinished accessor prefix", `tools.exec_command({cmd:"cat old", ge`, nil},
		{"non-shell batch", `const r=await Promise.allSettled(tasks);text(r)`, nil},
		{"only decoys", `const note="tools.exec_command({cmd:'fake'})"; // tools.exec_command({cmd:"also fake"})`, nil},
		{"regex decoy", `const matcher=/tools.exec_command\\({cmd:"fake"}\\)/;`, nil},
		{"qualified member decoy", `const fake={tools:{exec_command:x=>x}}; fake.tools.exec_command({cmd:"echo decoy"})`, nil},
		{"partial qualified member decoy", `fake./* gap */tools.exec_command({cmd:"echo decoy"`, nil},
		{"unicode identifier decoy", `const πtools={exec_command:x=>x}; πtools.exec_command({cmd:"echo decoy"})`, nil},
		{"unicode whitespace member decoy", "fake.\u00a0tools.exec_command({cmd:\"echo decoy\"", nil},
		{"BOM whitespace member decoy", "fake.\ufeff tools.exec_command({cmd:\"echo decoy\"", nil},
		{"other identifier start decoy", `const ℘tools={exec_command:x=>x}; ℘tools.exec_command({cmd:"echo decoy"})`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, call := range codeModeShellFragments(tc.source) {
				got = append(got, call.cmd)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("fragments = %#v; want %#v", got, tc.want)
			}
		})
	}
}

func TestCodeModeShellFragmentWorkdir(t *testing.T) {
	for _, tc := range []struct {
		name, source, workdir string
		dynamic               bool
	}{
		{"absent", `tools.exec_command({cmd:"cat > f <<'EOF'\nx`, "", false},
		{"literal after cmd", `tools.exec_command({cmd:"cat f",workdir:"/other"`, "/other", false},
		{"literal before cmd", `tools.exec_command({workdir:"/other",cmd:"cat > f <<'EOF'\nx`, "/other", false},
		{"unfinished literal", `tools.exec_command({cmd:"cat f",workdir:"/oth`, "", true},
		{"dynamic value", `tools.exec_command({cmd:"cat f",workdir:dir`, "", true},
		{"closed static object", `tools.exec_command({cmd:"cat f",workdir:"/other"})`, "/other", false},
		{"JSON arguments", `tools.exec_command({"cmd":"cat f","workdir":"/json"`, "/json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := codeModeShellFragments(tc.source)
			if len(calls) != 1 || calls[0].workdir != tc.workdir || calls[0].dynamicWorkdir != tc.dynamic {
				t.Fatalf("calls = %#v; want workdir %q, dynamic %t", calls, tc.workdir, tc.dynamic)
			}
		})
	}
}
