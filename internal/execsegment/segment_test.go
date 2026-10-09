package execsegment

import (
	"slices"
	"testing"
)

func TestSplitTopLevelLists(t *testing.T) {
	for _, tc := range []struct {
		script  string
		sources []string
	}{
		{"echo single", []string{"echo single"}},
		{"a | b", []string{"a | b"}},
		{"time make", []string{"time make"}},
		{"pwd && ls && cat a b", []string{"pwd", "ls", "cat a b"}},
		{"cd x; make test 2>&1 | tail -20", []string{"cd x", "make test 2>&1 | tail -20"}},
		{"a || b && ! c\nd", []string{"a", "b", "! c", "d"}},
		{"if true; then echo x; fi; echo y # note", []string{"if true; then echo x; fi", "echo y"}},
		{"f() { return 3; }; f", []string{"f() { return 3; }; f"}},
		{"cat <<'EOF' && ls\nbody\nEOF\n", []string{"cat <<'EOF'\nbody\nEOF", "ls"}},
		{"set -euo pipefail\necho ok", []string{"set -euo pipefail\necho ok"}},
		{"ROOT=$(mktemp -d); cat a; rg x src", []string{"ROOT=$(mktemp -d); cat a; rg x src"}},
		{"export ROOT=/tmp && cat a", []string{"export ROOT=/tmp && cat a"}},
		{"source setup.sh; cat a", []string{"source setup.sh; cat a"}},
		{". setup.sh; cat a", []string{". setup.sh; cat a"}},
		{"unset ROOT; cat a", []string{"unset ROOT; cat a"}},
		{"ROOT=/tmp cat a; cat b", []string{"ROOT=/tmp cat a", "cat b"}},
		{"echo \"$(ROOT=/tmp; echo x)\"; cat b", []string{"echo \"$(ROOT=/tmp; echo x)\"", "cat b"}},
		{"echo a; (export ROOT=/tmp; cat a); cat b", []string{"echo a", "(export ROOT=/tmp; cat a)", "cat b"}},
		{"exec 2>&1; echo ok", []string{"exec 2>&1", "echo ok"}},
	} {
		segments, ok := Split(tc.script)
		if !ok {
			t.Fatalf("Split(%q) declined", tc.script)
		}
		var sources []string
		for _, segment := range segments {
			sources = append(sources, segment.Source)
		}
		if !slices.Equal(sources, tc.sources) {
			t.Fatalf("Split(%q) = %q, want %q", tc.script, sources, tc.sources)
		}
	}
}

func TestSplitDeclinesUntrackableScripts(t *testing.T) {
	for _, script := range []string{
		"sleep 1 & wait",
		"a; wait",
		"trap 'echo x' EXIT; a",
		"return 1; a",
		"exec python app.py; a",
		"set -x; a",
		"a; echo ${PIPESTATUS[0]}",
		"a; echo $_",
		"coproc cat; a",
		"a; b 'unterminated",
		"__mekugi_b 0; a",
		"(cd x && make); a",
		"time make; a",
		"if (true); then a; fi; b",
		"{ (a); }; b",
	} {
		if segments, ok := Split(script); ok {
			t.Fatalf("Split(%q) = %v, want decline", script, segments)
		}
	}
}

func TestRewriteKeepsSourceBetweenHooks(t *testing.T) {
	script := "cat <<'EOF' && ls # list\nbody\nEOF\necho done"
	segments, ok := Split(script)
	if !ok {
		t.Fatal("declined")
	}
	want := "{ __mekugi_b 0 && :; cat <<'EOF'; __mekugi_e 0 && :; } && { __mekugi_b 1 && :; ls; __mekugi_e 1 && :; } # list\nbody\nEOF\n{ __mekugi_b 2 && :; echo done; __mekugi_e 2 && :; }"
	if got := Rewrite(script, segments); got != want {
		t.Fatalf("Rewrite = %q, want %q", got, want)
	}
}
