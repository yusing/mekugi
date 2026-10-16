package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/providers/opencode"

	"github.com/BurntSushi/toml"
)

type mekugiConfig struct {
	Providers    opencode.Config   `toml:"providers"`
	ServiceTiers map[string]string `toml:"service_tiers"`
}

func loadMekugiConfig(withProviders bool) (mekugiConfig, error) {
	var config mekugiConfig
	directory, err := os.UserConfigDir()
	if err != nil {
		// Without a configuration directory there can be no file to load.
		// Preserve environment-only setup and the existing startup diagnostics.
		if !withProviders {
			return config, nil
		}
		config.Providers, err = opencode.Environment(opencode.Config{})
		return config, err
	}
	body, err := os.ReadFile(filepath.Join(directory, "mekugi", "config.toml"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return mekugiConfig{}, errors.New("cannot read Mekugi config.toml")
	}
	if err == nil {
		metadata, err := toml.Decode(string(body), &config)
		if err != nil {
			// TOML errors can quote a line containing a credential.
			return mekugiConfig{}, errors.New("invalid Mekugi config.toml")
		}
		if len(metadata.Undecoded()) != 0 {
			return mekugiConfig{}, errors.New("unknown setting in Mekugi config.toml")
		}
	}
	for model, tier := range config.ServiceTiers {
		if strings.TrimSpace(model) == "" || model != strings.TrimSpace(model) {
			return mekugiConfig{}, errors.New("invalid model in Mekugi service_tiers")
		}
		switch tier {
		case "fast":
			config.ServiceTiers[model] = "priority"
		case "priority", "default", "auto", "flex":
		default:
			return mekugiConfig{}, errors.New("invalid service tier in Mekugi config.toml")
		}
	}
	if !withProviders {
		config.Providers = opencode.Config{}
		return config, nil
	}
	config.Providers, err = opencode.Environment(config.Providers)
	return config, err
}
