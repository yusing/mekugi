// Source: apps/mekugi/cmd/mekugi/third_party.go:1:56@[ebde9ad5]
package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/apps/mekugi/internal/launch/modelcatalog"
)

// Explicit invocation models win over Grok's default, which wins over the
// latest standard model. Codex's own configured default is not a Grok default.
func thirdPartyDefaultArgs(args []string, selection modelcatalog.Selection) ([]string, error) {
	if launchoptions.HasModelOverride(args) {
		return args, nil
	}
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	model := ""
	if selection.GrokEnabled {
		model = "grok-4.7"
	}
	if home, err := os.UserHomeDir(); err == nil && selection.GrokEnabled {
		var config struct {
			Models struct {
				Default string `toml:"default"`
			} `toml:"models"`
		}
		_, err := toml.DecodeFile(filepath.Join(home, ".grok", "config.toml"), &config)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("could not read Grok configuration (~/.grok/config.toml); fix it or select a model with -m")
		}
		if value := strings.TrimSpace(config.Models.Default); value != "" {
			model = value
		}
	}
	if model != "" {
		model = strings.TrimPrefix(model, "grok:")
		if !selection.GrokUnprefixed {
			model = "grok:" + model
		}
	}
	if model == "" {
		model = selection.OpenCode.DefaultModel()
		if model == "" {
			return nil, errors.New("authenticated third-party providers have no available models")
		}
	}
	return slices.Insert(slices.Clone(args), index, "-c", fmt.Sprintf("model=%q", model)), nil
}
