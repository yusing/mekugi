package main

import (
	"slices"
	"testing"

	"github.com/yusing/mekugi/backends/codex/shellhook"

	"github.com/BurntSushi/toml"
)

func TestOptionalTrackingHookArgsPreservesExistingTrustAndCaller(t *testing.T) {
	caller := []string{"exec", "-c", `hooks.state={other={trusted_hash="existing"}}`, "--", "-c", "hooks.PreToolUse=[]"}
	original := slices.Clone(caller)
	got, identity, err := optionalTrackingHookArgs(caller, "/path with spaces/mekugi", "/private/tracker")
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
	if len(config.Hooks.State) != 2 || config.Hooks.State["other"].TrustedHash != "existing" {
		t.Fatalf("existing hook trust lost: %+v", config.Hooks.State)
	}
	guard := config.Hooks.State[shellhook.Key]
	if identity == "" || guard.TrustedHash != identity || guard.Enabled == nil || !*guard.Enabled {
		t.Fatalf("tracking hook trust or enablement missing: %+v", guard)
	}
}

func TestOptionalTrackingHookArgsRejectsExplicitConflicts(t *testing.T) {
	for _, setting := range []string{
		"hooks={PreToolUse=[]}", "hooks.PreToolUse=[]", "hooks.PreToolUse.0.matcher='Bash'",
		`hooks.state={"/<session-flags>/config.toml:pre_tool_use:0:0"={enabled=false}}`,
		"hooks.state={broken",
	} {
		for _, args := range [][]string{{"exec", "-c", setting}, {"exec", "--config=" + setting}, {"exec", "-c" + setting}} {
			original := slices.Clone(args)
			fallback, _, err := optionalTrackingHookArgs(args, "/mekugi", "/tracker")
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
