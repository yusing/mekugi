// Source: apps/mekugi/cmd/mekugi/shell_hook.go:1:53@[ebde9ad5]
package launch

import (
	json "encoding/json/v2"
	"fmt"
	"slices"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"

	"github.com/yusing/mekugi/backends/codex/shellhook"
)

// Append tracking after caller hooks, preserving their native order and trust.
func optionalTrackingHookArgs(args []string, helper, tracker, capture string) ([]string, string, error) {
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	prior := shellhook.Invocation{}
	if hooks, exists := launchoptions.ConfigOverrides(args)["hooks"]; exists {
		data, err := json.Marshal(hooks)
		if err != nil {
			return args, "", err
		}
		if err := json.Unmarshal(data, &prior); err != nil {
			return args, "", fmt.Errorf("read CLI hooks: %w", err)
		}
	}
	config, state, hash, err := shellhook.Config(helper, tracker, capture, prior)
	if err != nil {
		return args, "", err
	}
	return slices.Insert(slices.Clone(args), index, "-c", config, "-c", state), hash, nil
}
