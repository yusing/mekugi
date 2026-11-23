package shellinstrumentation

import (
	"slices"
	"testing"
)

func TestCommandScriptPreservesLiteralShellSource(t *testing.T) {
	for _, test := range []struct{ command, script string }{
		{`/bin/bash -lc 'printf "a  b\n"; echo "$HOME"'`, `printf "a  b\n"; echo "$HOME"`},
		{`sh -c "printf 'x y'"`, `printf 'x y'`},
		{`/usr/bin/dash -c ''`, ""},
	} {
		got, ok := CommandScript(test.command)
		if !ok || got != test.script {
			t.Errorf("CommandScript(%q) = %q, %v; want %q", test.command, got, ok, test.script)
		}
	}
	args, ok := CommandArgs(`bash -c 'printf literal'`)
	if !ok || !slices.Equal(args, []string{"bash", "-c", "printf literal"}) {
		t.Fatalf("literal command argv changed: %q, %v", args, ok)
	}
}

func TestCommandScriptRejectsDynamicOrExtraArgv(t *testing.T) {
	for _, command := range []string{
		`bash -c "$SCRIPT"`,
		`bash -c "$(echo true)"`,
		`bash -c true extra`,
		`bash -c true; sh -c false`,
		`MODE=test bash -c true`,
		`bash -c true > output`,
		`bash -c true &`,
		`! bash -c true`,
		`python -c 'print(1)'`,
		`bash --command true`,
		`bash -c`,
	} {
		if script, ok := CommandScript(command); ok {
			t.Errorf("accepted nonliteral or unsupported shell argv %q as %q", command, script)
		}
	}
}

func TestCommandSourceRestoresInlineBoundaryVersions(t *testing.T) {
	const script = "printf one; printf two"
	const tracker = "/path ] || . with ' quote/tracker.sh"
	legacy := ":; __mekugi_b() { return $?; }; __mekugi_e() { return $?; }; [ ! -r '/prior/tracker' ] || . '/prior/tracker'; { __mekugi_b 0 && :; printf one; __mekugi_e 0 && :; }; { __mekugi_b 1 && :; printf two; __mekugi_e 1 && :; }"
	for _, wrapped := range []string{legacy, ShScript(tracker, script)} {
		if source := ShOriginal(wrapped); source != script {
			t.Fatalf("retained command source = %q", source)
		}
	}
}
