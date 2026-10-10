package main

import (
	"slices"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/internal/vcsguard"
)

func TestVCSGuardHookArgsPreservesRecoveryTrustAndCaller(t *testing.T) {
	caller, registered := postCompactHookArgs([]string{"exec", "--", "-c", "hooks.PreToolUse=[]"}, "/mekugi")
	if !registered {
		t.Fatal("recovery hook not registered")
	}
	original := slices.Clone(caller)
	got, identity, err := vcsGuardHookArgs(caller, "/path with spaces/mekugi", "/private/guard", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(caller, original) {
		t.Fatalf("caller mutated: %q", caller)
	}
	index := slices.Index(caller, "--")
	if !slices.Equal(got[:index], caller[:index]) || !slices.Equal(got[index+4:], caller[index:]) {
		t.Fatalf("base argv changed: %q", got)
	}
	var config struct {
		Hooks struct {
			State map[string]struct {
				TrustedHash string `toml:"trusted_hash"`
				Enabled     *bool  `toml:"enabled"`
			} `toml:"state"`
		} `toml:"hooks"`
	}
	if _, err := toml.Decode(got[index+3], &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Hooks.State) != 2 || config.Hooks.State[postCompactHookKey].TrustedHash == "" {
		t.Fatalf("recovery trust lost: %+v", config.Hooks.State)
	}
	guard := config.Hooks.State[vcsguard.HookKey]
	// Reported as trusted by `codex app-server` hooks/list for this exact hook.
	const hash = "sha256:16ebe25cea81aca7a47215fe1edd9a5c2bcdb9ed2c6a520a3ab8b3f0d3f56048"
	if guard.TrustedHash != hash || identity != hash || guard.Enabled != nil {
		t.Fatalf("guard trust identity or enablement changed: %+v", guard)
	}
}

func TestVCSGuardHookArgsRejectsExplicitConflicts(t *testing.T) {
	for _, setting := range []string{
		"hooks={PreToolUse=[]}", "hooks.PreToolUse=[]", "hooks.PreToolUse.0.matcher='Bash'",
		`hooks.state={"/<session-flags>/config.toml:pre_tool_use:0:0"={enabled=false}}`,
		"hooks.state={broken",
	} {
		for _, args := range [][]string{{"exec", "-c", setting}, {"exec", "--config=" + setting}, {"exec", "-c" + setting}} {
			original := slices.Clone(args)
			fallback, _, err := vcsGuardHookArgs(args, "/mekugi", "", "", "")
			if err == nil {
				t.Errorf("accepted conflicting config: %q", args)
			}
			if !slices.Equal(fallback, original) {
				t.Fatalf("fallback lost launch arguments: %q", fallback)
			}
			if !slices.Equal(args, original) {
				t.Fatalf("conflict mutated caller: %q", args)
			}
		}
	}
}
