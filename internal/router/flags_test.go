package router

import (
	"io"
	"slices"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	for _, prefix := range [][]string{
		nil,
		{"--debug"},
		{"--vcs-guard"},
		{"--vcs-guard=false"},
		{"--duplicate-output"},
		{"--duplicate-output=false"},
		{"--ansi-faint=off"},
		{"--ansi-faint", "on"},
		{"--ansi-faint=auto"},
		{"--debug", "--capture-output", "capture.jsonl"},
		{"--grok-auth-file", "auth.json"},
		{"--timeout", "30s"},
		{"--capture-output", "wrap"},
		{"--capture-output", "--"},
	} {
		command := []string{"codex", "exec", "--model", "example", "--", "wrap"}
		args := append(slices.Clone(prefix), command...)
		gotPrefix, gotCommand, err := SplitCommand(args)
		if err != nil || !slices.Equal(gotPrefix, prefix) || !slices.Equal(gotCommand, command) {
			t.Errorf("SplitCommand(%q) = %q, %q, %v", args, gotPrefix, gotCommand, err)
		}
	}
}

func TestDuplicateOutputFlagOptIn(t *testing.T) {
	flags := newRouterFlags(io.Discard)
	if *flags.duplicateOutput {
		t.Fatal("duplicate output projection must default off")
	}
	if err := flags.Parse([]string{"--duplicate-output"}); err != nil || !*flags.duplicateOutput {
		t.Fatalf("opt-in = %v, %v", *flags.duplicateOutput, err)
	}
}

func TestSplitCommandRestrictionsAndDelimiter(t *testing.T) {
	for _, args := range [][]string{
		{"--grok", "codex"},
		{"--grok=false", "codex"},
		{"--timeout", "not-a-duration", "grok"},
	} {
		if _, _, err := SplitCommand(args); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	prefix, command, err := SplitCommand([]string{"--debug", "--", "grok"})
	if err != nil || !slices.Equal(prefix, []string{"--debug", "--"}) || !slices.Equal(command, []string{"grok"}) {
		t.Fatalf("delimiter = %q, %q, %v", prefix, command, err)
	}
}

func TestSplitCommandStandalone(t *testing.T) {
	for _, args := range [][]string{nil, {"--yolo", "-m", "grok:grok-4.7"}, {"exec", "--yolo", "hello"}, {"resume", "--last"}} {
		prefix, command, err := SplitCommand(append([]string{"--debug"}, args...))
		if err != nil || !slices.Equal(prefix, []string{"--debug"}) || !slices.Equal(command, args) {
			t.Fatalf("standalone split = %q, %q, %v", prefix, command, err)
		}
	}
}

func TestHasModelOverridePreservesOperands(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"-m", "grok-4.5"}, true},
		{[]string{"--model=grok-4.5"}, true},
		{[]string{"-mgrok-4.5"}, true},
		{[]string{"-c", `"model"="grok-4.5"`}, true},
		{[]string{"--config=model='grok-4.5'"}, true},
		{[]string{"-cmodel='grok-4.5'"}, true},
		{[]string{"--image", "-model.png"}, false},
		{[]string{"--output-schema", "-model.json"}, false},
		{[]string{"-o", "-model-output.txt"}, false},
		{[]string{"--thread-source", "-model-source"}, false},
		{[]string{"-c", "other=-model"}, false},
		{[]string{"--profile", "-model-profile", "--", "-m", "literal"}, false},
	} {
		if got := HasModelOverride(tc.args); got != tc.want {
			t.Fatalf("HasModelOverride(%q) = %t, want %t", tc.args, got, tc.want)
		}
	}
}

func TestVCSGuardFlagAndStandaloneSplit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix []string
		want   bool
	}{
		{name: "default", want: true},
		{name: "enabled", prefix: []string{"--vcs-guard"}, want: true},
		{name: "disabled", prefix: []string{"--vcs-guard=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := []string{"--yolo", "-m", "example"}
			prefix, forwarded, err := SplitCommand(append(slices.Clone(tc.prefix), command...))
			if err != nil || !slices.Equal(prefix, tc.prefix) || !slices.Equal(forwarded, command) {
				t.Fatalf("split = %q, %q, %v", prefix, forwarded, err)
			}
			flags := newRouterFlags(io.Discard)
			if err := flags.Parse(prefix); err != nil {
				t.Fatal(err)
			}
			if *flags.vcsGuard != tc.want {
				t.Fatalf("guard = %t, want %t", *flags.vcsGuard, tc.want)
			}
		})
	}
}
