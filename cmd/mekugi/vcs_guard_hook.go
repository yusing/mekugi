package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/internal/vcsguard"
)

// vcsGuardHookArgs adds the guard to this invocation. Preserve other events
// and trust entries, including the session recovery hook registered earlier.
func vcsGuardHookArgs(args []string, helper, directory string) ([]string, error) {
	config, state, err := vcsguard.HookConfig(helper, directory)
	if err != nil {
		return args, err
	}
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	previousState := ""
	for i := 0; i < index; i++ {
		setting, _ := codexArgumentValue(args[:index], &i, "-c", "--config")
		key, value, _ := strings.Cut(setting, "=")
		key = strings.TrimSpace(key)
		if key == "hooks" || key == "hooks.PreToolUse" || strings.HasPrefix(key, "hooks.PreToolUse.") {
			return args, fmt.Errorf("command hook conflicts with explicit CLI PreToolUse configuration")
		}
		if key == "hooks.state" {
			var parsed struct {
				State map[string]any `toml:"state"`
			}
			if _, err := toml.Decode("state="+value, &parsed); err != nil {
				return args, fmt.Errorf("read CLI hook state: %w", err)
			}
			if _, exists := parsed.State[vcsguard.HookKey]; exists {
				return args, fmt.Errorf("explicit CLI state for the command hook conflicts")
			}
			value = strings.TrimSpace(value)
			if !strings.HasPrefix(value, "{") || !strings.HasSuffix(value, "}") {
				return args, fmt.Errorf("CLI hook state must be a table")
			}
			previousState = strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	if previousState != "" {
		state = previousState + "," + state
	}
	return slices.Insert(slices.Clone(args), index, "-c", config, "-c", "hooks.state={"+state+"}"), nil
}
