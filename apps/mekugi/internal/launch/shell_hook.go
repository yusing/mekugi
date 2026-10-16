// Source: apps/mekugi/cmd/mekugi/shell_hook.go:1:53@[ebde9ad5]
package launch

import (
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/backends/codex/shellhook"

	"github.com/BurntSushi/toml"
)

// optionalTrackingHookArgs preserves caller hooks when shell guarding is off.
// Tracking remains auxiliary and does not start a discovery process.
func optionalTrackingHookArgs(args []string, helper, tracker string) ([]string, string, error) {
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	prior := shellhook.Invocation{}
	for i := 0; i < index; i++ {
		setting, _ := codexArgumentValue(args[:index], &i, "-c", "--config")
		key, value, _ := strings.Cut(setting, "=")
		key = strings.TrimSpace(key)
		if key == "hooks" || key == "hooks.PreToolUse" || strings.HasPrefix(key, "hooks.PreToolUse.") {
			return args, "", fmt.Errorf("shell hook conflicts with explicit CLI PreToolUse configuration")
		}
		if key == "hooks.state" {
			var parsed struct {
				State map[string]any `toml:"state"`
			}
			if _, err := toml.Decode("state="+value, &parsed); err != nil {
				return args, "", fmt.Errorf("read CLI hook state: %w", err)
			}
			if _, exists := parsed.State[shellhook.Key]; exists {
				return args, "", fmt.Errorf("explicit CLI state for the shell hook conflicts")
			}
			data, err := json.Marshal(&parsed.State)
			if err != nil {
				return args, "", err
			}
			if err := json.Unmarshal(data, &prior.State); err != nil {
				return args, "", err
			}
		}
	}
	config, state, hash, err := shellhook.Config(helper, "", "", tracker, prior, nil)
	if err != nil {
		return args, "", err
	}
	return slices.Insert(slices.Clone(args), index, "-c", config, "-c", state), hash, nil
}
