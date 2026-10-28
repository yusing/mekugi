package presentation

import (
	"strings"
	"testing"
	"time"
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

func TestMRunGenericCommandDisplay(t *testing.T) {
	for _, tc := range []struct{ source, child string }{
		{`mrun --max-tokens 6500 -- foo 'two words'`, `foo 'two words'`},
		{`mrun --tail -n 35 make test`, `make test`},
		{`mrun -n 2 mrun --max-tokens=4000 -- foo "$value" > result`, `foo "$value" > result`},
		{`env env -u BASH_ENV mrun -n 2 foo`, `env env -u BASH_ENV foo`},
		{`mrun -n 2 foo | mrun -n 1 bar`, `foo | bar`},
		{`mrun -n 2 foo; mrun -n 1 bar`, `foo; bar`},
		{`mrun -n 2 foo; cat a`, `foo; cat a`},
		{"mrun -n 2 python3 - <<'PY'\nprint('hello')\nPY", "python3 - <<'PY'\nprint('hello')\nPY"},
		{"cat <<EOF; mrun -n 2 foo\n$(mrun -n 1 bar)\nEOF", "cat <<EOF; foo\n$(bar)\nEOF"},
		{"cat <<EOF; mrun -n 2 foo\n$(mrun -n 1 very_long_child_name)\nEOF", "cat <<EOF; foo\n$(very_long_child_name)\nEOF"},
	} {
		if got, want := ShellSource(tc.source, ""), ShellSource(tc.child, ""); got != want {
			t.Errorf("display %q = %q, want %q", tc.source, got, want)
		}
		content := ItemContent{Kind: "tool", Text: ShellSource(tc.source, ""),
			Command: Command{Script: tc.source, Display: tc.source, Duration: time.Second, Exited: true},
			Details: &ItemDetails{CommandText: tc.source}}
		for _, value := range content.Values() {
			if strings.Contains(value.Code, "mrun ") {
				t.Errorf("published wrapper for %q: %+v", tc.source, value)
			}
		}
		if tc.child == "foo; bar" && len(content.Values()) != 1 {
			t.Errorf("unwrapping duplicated combined command: %+v", content.Values())
		}
	}
}
