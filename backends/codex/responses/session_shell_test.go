package responses

import (
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestRequestSessionShell(t *testing.T) {
	context := func(body string) map[string]any {
		return map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "<environment_context>\n" + body + "\n</environment_context>"}}}
	}
	for _, test := range []struct {
		name  string
		input []any
		want  string
	}{
		{"single", []any{context("  <shell>bash</shell>"), map[string]any{"role": "user", "content": "task"}}, "bash"},
		{"update keeps the shell", []any{context("  <shell>zsh</shell>"), context("  <current_date>2026-09-24</current_date>")}, "zsh"},
		{"latest wins", []any{context("  <shell>zsh</shell>"), context("  <shell>bash</shell>")}, "bash"},
		{"environments disagree", []any{context("  <environments>\n    <shell>bash</shell>\n    <shell>powershell</shell>\n  </environments>")}, ""},
		{"quoted in a message", []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "see <shell>bash</shell>"}}}}, ""},
	} {
		if got := SessionShell(responses.MustMarshalJSON(test.input)); got != test.want {
			t.Errorf("%s: shell = %q, want %q", test.name, got, test.want)
		}
	}

}
