package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPostCompactHookArgsAddsSessionHookWithoutMutatingCaller(t *testing.T) {
	caller := []string{"exec", "prompt"}
	original := slices.Clone(caller)
	got, registered := postCompactHookArgs(caller, "/path with spaces/mekugi")
	if !registered {
		t.Fatal("hook was not registered")
	}
	if !slices.Equal(caller, original) {
		t.Fatalf("caller-owned arguments mutated: got %q, original %q", caller, original)
	}
	if len(got) != len(caller)+2 || !slices.Equal(got[:len(caller)], caller) || got[len(caller)] != "-c" || got[len(caller)+1] == "" {
		t.Fatalf("hook args not added without changing caller arguments: %q", got)
	}
	if !strings.Contains(got[len(caller)+1], "hooks.SessionStart") || !strings.Contains(got[len(caller)+1], "matcher=\"^compact$\"") {
		t.Fatalf("missing native compact SessionStart hook: %q", got[len(caller)+1])
	}
}

func TestPostCompactHookArgsShellQuotesExecutable(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable")
	}
	executable := filepath.Join(t.TempDir(), "mekugi ' test")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	args, registered := postCompactHookArgs([]string{"exec"}, executable)
	if !registered {
		t.Fatal("hook was not registered")
	}
	var config struct {
		Hooks struct {
			SessionStart []struct {
				Matcher string `toml:"matcher"`
				Hooks   []struct {
					Type    string `toml:"type"`
					Command string `toml:"command"`
				} `toml:"hooks"`
			} `toml:"SessionStart"`
		} `toml:"hooks"`
	}
	if _, err := toml.Decode(args[2], &config); err != nil {
		t.Fatalf("decode hook config %q: %v", args[2], err)
	}
	if len(config.Hooks.SessionStart) != 1 || len(config.Hooks.SessionStart[0].Hooks) != 1 {
		t.Fatalf("unexpected hook structure: %+v", config.Hooks)
	}
	hook := config.Hooks.SessionStart[0]
	if hook.Matcher != "^compact$" || hook.Hooks[0].Type != "command" {
		t.Fatalf("unexpected hook metadata: %+v", hook)
	}
	command := exec.Command(shell, "-c", hook.Hooks[0].Command)
	output, err := command.Output()
	if err != nil || string(output) != "post-compact\n" {
		t.Fatalf("shell command %q output %q: %v", hook.Hooks[0].Command, output, err)
	}
}

func TestPostCompactHookArgsRetainsExplicitHooksConfiguration(t *testing.T) {
	for _, caller := range [][]string{
		{"-c", "hooks.SessionStart=[]", "exec"},
		{"--config=hooks.SessionStart=[]", "exec"},
		{"-chooks.SessionStart=[]", "exec"},
		{"-c=hooks.SessionStart=[]", "exec"},
		{"-c=hooks={SessionStart=[]}", "exec"},
	} {
		original := slices.Clone(caller)
		got, registered := postCompactHookArgs(caller, "/mekugi")
		if registered {
			t.Errorf("explicit hooks unexpectedly overwritten for %q: %q", caller, got)
		}
		if !slices.Equal(got, caller) || !slices.Equal(caller, original) {
			t.Errorf("skip changed caller args: got %q, caller %q", got, caller)
		}
	}
}

func TestPostCompactHookArgsDoesNotTreatAfterDelimiterAsCLIConfig(t *testing.T) {
	caller := []string{"exec", "--", "-c", "hooks.SessionStart=[]", "prompt"}
	got, registered := postCompactHookArgs(caller, "/mekugi")
	if !registered {
		t.Fatal("prompt text after -- incorrectly suppressed hook registration")
	}
	delimiter := slices.Index(got, "--")
	if delimiter != 3 || !slices.Equal(got[delimiter+1:], caller[2:]) {
		t.Fatalf("injection did not preserve positional text after --: %q", got)
	}
}

func TestPostCompactHookArgsIgnoresOtherConfigAndAddsSessionHook(t *testing.T) {
	caller := []string{"exec", "-c", "model='example'", "--config=profile='work'", "prompt"}
	got, registered := postCompactHookArgs(caller, "/mekugi")
	if !registered {
		t.Fatal("unrelated CLI config prevented hook registration")
	}
	if len(got) != len(caller)+2 || !slices.Equal(got[:len(caller)], caller) {
		t.Fatalf("caller options changed or hook inserted in wrong position: %q", got)
	}
	if caller[1] != "-c" || caller[2] != "model='example'" {
		t.Fatalf("original config was modified: %q", caller)
	}
}
