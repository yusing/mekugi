package execsegment

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/shellsyntax"
)

func TestClaudeWrapperKeepsNativeSetupAndCwdWrite(t *testing.T) {
	for _, script := range []string{"printf 'one\\n'; printf 'two\\n'", "printf 'it\\x27s quoted\\n'; false || printf 'recovered\\n'", "cat <<'EOF'; printf tail\nbody\nEOF\n", "printf '$HOME * { ? }\\n'; printf '你好\\n'"} {
		prefix := "source '/tmp/snapshot' 2>/dev/null || true && export NATIVE_SETUP=kept && eval "
		suffix := " < /dev/null && pwd -P >| '/tmp/claude-ab12-cwd'"
		wrapper := prefix + shellsyntax.Quote(script) + suffix
		got, rewritten, segments, ok := ClaudeWrapper(wrapper)
		if !ok || got != script || len(segments) < 2 || rewritten != prefix+shellsyntax.Quote(Rewrite(script, segments))+suffix {
			t.Fatalf("wrapper = %q, %q, %+v, %v", got, rewritten, segments, ok)
		}
	}
	// Native shell quoting can use a double-quoted literal without expansions.
	script, _, _, ok := ClaudeWrapper(`eval "printf '*?{}\\n'; printf 'literal\\n'" && pwd -P >| /tmp/cwd-ab12`)
	if !ok || !strings.Contains(script, "*?{}") {
		t.Fatalf("double-quoted literal was not decoded: %q, %v", script, ok)
	}
}

func TestClaudeWrapperDeclinesUnprovenOperands(t *testing.T) {
	for _, wrapper := range []string{
		`eval "$PAYLOAD" && pwd -P >| /tmp/claude-ab12-cwd`,
		`eval "$(printf payload)" && pwd -P >| /tmp/claude-ab12-cwd`,
		`eval 'printf one; printf two' more && pwd -P >| /tmp/claude-ab12-cwd`,
		`eval 'printf one; printf two' || pwd -P >| /tmp/claude-ab12-cwd`,
		`eval 'printf one; printf two' && pwd -P >| "$CWD"`,
		`eval 'printf one; printf two' && pwd -P >| /tmp/unrelated`,
		`eval 'printf one; printf two' < /tmp/input && pwd -P >| /tmp/claude-ab12-cwd`,
		`eval 'printf one; printf two' && pwd -P > /tmp/claude-ab12-cwd`,
		`eval 'printf one; printf two' && pwd -P >| /tmp/claude-ab12-cwd; printf suffix`,
		`eval 'printf one' && pwd -P >| /tmp/claude-ab12-cwd`,
		`eval 'trap echo EXIT; printf two' && pwd -P >| /tmp/claude-ab12-cwd`,
	} {
		if _, _, _, ok := ClaudeWrapper(wrapper); ok {
			t.Errorf("accepted unproven wrapper %q", wrapper)
		}
	}
}
