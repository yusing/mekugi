package router

import (
	"strings"
	"testing"
)

func TestSearchDisplayQueryAndTarget(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{`rg -n -F -e 'some text' a.go`, "Search `some text` in `a.go`"},
		{`rg -n 'BUG|FIX' -S | head -n 200`, "Search `BUG|FIX`"},
		{`rg -n codex_api codex-rs -S | head -n 50`, "Search `codex_api` in `codex-rs`"},
		{`rg --colors=never -g '*.go' -t go -m 2 -A 1 --max-depth 3 needle src`, "Search `needle` in `src`"},
		{`rg --regexp='a/b' -e second src other | sort -u | head`, "Search `a/b | second` in `src` `other`"},
		{`rg needle src/a.go 'lib/with space.go'`, "Search `needle` in `src/a.go` `lib/with space.go`"},
		{`rg -efoo -- -path`, "Search `foo` in `-path`"},
		{`rg -- -needle src`, "Search `-needle` in `src`"},
		{`grep -nE 'a/b' src | tail -n 10`, "Search `a/b` in `src`"},
		{`grep -E 'a/b' src`, "Search `a/b` in `src`"},
		{`grep -nefoo src`, "Search `foo` in `src`"},
		{`grep --color needle src`, "Search `needle` in `src`"},
		{`rg --replace changed needle src`, "Search `needle` in `src`"},
		{`rg -r changed needle src`, "Search `needle` in `src`"},
		{`rg --pre cat needle src`, "Search `needle` in `src`"},
		{`rg -nA2 -g'*.go' needle src`, "Search `needle` in `src`"},
		{`grep --regexp=needle --include='*.go' src 2>/dev/null | head -5`, "Search `needle` in `src`"},
		{`rg --files src | head -n 50`, "List `src`"},
		{`rg -n first src | grep -v second | head -n 5`, "Search `first` in `src`\n\nSearch `second`"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			item := stockExecDisplayItem(tc.source)
			if got := subagentToolActivityText(item, "exec_command"); got != tc.want {
				t.Fatalf("native display = %q, want %q", got, tc.want)
			}
			source := `text(await tools.exec_command({cmd:` + string(mustMarshalJSON(tc.source)) + `}));`
			calls, ok := toolActivityUnwrapExecCalls(source, false)
			if !ok || len(calls) != 1 || subagentToolActivityText(calls[0], "exec_command") != tc.want {
				t.Fatalf("Code Mode display did not preserve query/target: %v", calls)
			}
		})
	}
	for _, source := range []string{
		`rg -n needle src | head file.txt`,
		`rg -n needle src | tee saved.txt`,
		`rg -n needle src | head -n "$count"`,
		`rg -n needle src > result.txt`,
		`rg -n needle src | sort -o saved.txt`,
		`rg -e`,
		`rg --unknown-option value needle src`,
	} {
		if got := toolActivityShell(source); !strings.HasPrefix(got, "Run") || !strings.Contains(got, source) {
			t.Errorf("unsafe/unknown pipeline lost original source: %q", got)
		}
	}
}
