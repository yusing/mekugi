package options

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	for _, prefix := range [][]string{
		nil,
		{"--debug"},
		{"--shell-guard"},
		{"--shell-guard=false"},
		{"--deduplicate-output"},
		{"--deduplicate-output=false"},
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

func TestDuplicateOutputFlagDefaultAndOptOut(t *testing.T) {
	config := new(Config)
	flags := newFlags(io.Discard, config)
	if !config.DeduplicateOutput {
		t.Fatal("duplicate output projection must default on")
	}
	if err := flags.Parse([]string{"--deduplicate-output=false"}); err != nil || config.DeduplicateOutput {
		t.Fatalf("opt-out = %v, %v", config.DeduplicateOutput, err)
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

func TestShellGuardFlagAndStandaloneSplit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix []string
		want   bool
	}{
		{name: "default", want: true},
		{name: "enabled", prefix: []string{"--shell-guard"}, want: true},
		{name: "disabled", prefix: []string{"--shell-guard=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := []string{"--yolo", "-m", "example"}
			prefix, forwarded, err := SplitCommand(append(slices.Clone(tc.prefix), command...))
			if err != nil || !slices.Equal(prefix, tc.prefix) || !slices.Equal(forwarded, command) {
				t.Fatalf("split = %q, %q, %v", prefix, forwarded, err)
			}
			config := new(Config)
			flags := newFlags(io.Discard, config)
			if err := flags.Parse(prefix); err != nil {
				t.Fatal(err)
			}
			if config.ShellGuard != tc.want {
				t.Fatalf("guard = %t, want %t", config.ShellGuard, tc.want)
			}
		})
	}
}

func TestPublicOptionsExposeOneInterface(t *testing.T) {
	config, err := Parse(nil)
	if err != nil || !config.FrontendsEnabled() {
		t.Fatalf("default interface: config=%+v err=%v", config, err)
	}
	for _, mode := range []string{"mekugi", "passthrough", "direct"} {
		if _, err := Parse([]string{"--mode", mode}); err == nil {
			t.Fatalf("accepted response-mode selector %q", mode)
		}
	}
	var help bytes.Buffer
	flags := newFlags(&help, new(Config))
	flags.Usage()
	for _, command := range []string{"mekugi server [--socket PATH]", "mekugi connector [--listen ADDRESS]"} {
		if !strings.Contains(help.String(), command) {
			t.Fatalf("help omits control entrypoint %q: %s", command, help.String())
		}
	}
	if strings.Contains(help.String(), "passthrough") || strings.Contains(help.String(), "-mode") {
		t.Fatalf("help exposes an internal mode: %s", help.String())
	}
}

func TestFeatureFlagsPreserveSyntaxAndHostArguments(t *testing.T) {
	for _, name := range []string{"shell-guard", "deduplicate-output", "child-reply-summary", "debug"} {
		if _, err := Parse([]string{"--" + name + "=invalid"}); err == nil {
			t.Fatalf("accepted invalid %s", name)
		}
		if _, _, err := SplitCommand([]string{"--" + name + "=invalid", "codex"}); err == nil {
			t.Fatalf("split accepted invalid %s", name)
		}
		args := []string{"--" + name + "=false", "--", "--" + name, "--enable", "host-feature"}
		prefix, command, err := SplitCommand(args)
		if err != nil || !slices.Equal(prefix, args[:2]) || !slices.Equal(command, args[2:]) {
			t.Fatalf("delimiter changed: %v %v %v", prefix, command, err)
		}
		parsed, err := Parse(prefix)
		if err != nil {
			t.Fatal(err)
		}
		resolved := parsed.ResolveFeatures(Features{ShellGuard: new(true), DeduplicateOutput: new(true), ChildReplySummary: new(true), Debug: new(true)})
		if name == "shell-guard" && resolved.ShellGuard || name == "deduplicate-output" && resolved.DeduplicateOutput || name == "debug" && resolved.Debug || name == "child-reply-summary" && resolved.ChildReplySummary {
			t.Fatal("delimiter lost explicit false")
		}
	}
}
