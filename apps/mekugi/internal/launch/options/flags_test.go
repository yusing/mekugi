package options

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	for _, prefix := range [][]string{
		nil,
		{"--debug"},
		{"--child-reply-summary"},
		{"--child-reply-summary=false"},
		{"--ansi-faint=off"},
		{"--ansi-faint", "on"},
		{"--ansi-faint=auto"},
		{"--debug", "--capture-output", "capture.jsonl"},
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

func TestSplitCommandStandalone(t *testing.T) {
	for _, args := range [][]string{nil, {"exec", "--yolo", "hello"}, {"resume", "--last"}} {
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

func TestPublicOptionsExposeOneInterface(t *testing.T) {
	config, err := Parse(nil)
	if err != nil {
		t.Fatalf("default interface: config=%+v err=%v", config, err)
	}
	var help bytes.Buffer
	flags := newFlags(&help, new(Config))
	flags.Usage()
	for _, command := range []string{"mekugi server [--socket PATH]", "mekugi connector [--listen ADDRESS]", "mekugi web [--listen ADDRESS]"} {
		if !strings.Contains(help.String(), command) {
			t.Fatalf("help omits control entrypoint %q: %s", command, help.String())
		}
	}
}

func TestFeatureFlagsPreserveSyntaxAndHostArguments(t *testing.T) {
	for _, name := range []string{"child-reply-summary", "debug"} {
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
		resolved := parsed.ResolveFeatures(Features{ChildReplySummary: new(true), Debug: new(true)})
		if name == "debug" && resolved.Debug || name == "child-reply-summary" && resolved.ChildReplySummary {
			t.Fatal("delimiter lost explicit false")
		}
	}
}
