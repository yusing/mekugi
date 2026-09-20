package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// OpenCodeConfig enables each independently authenticated service when its key
// is nonempty. It is invocation-local; loading never writes user configuration.
type OpenCodeConfig struct {
	catalog *openCodeCatalog
	Go      OpenCodeServiceConfig `toml:"opencode_go"`
	Zen     OpenCodeServiceConfig `toml:"opencode_zen"`
}

type OpenCodeServiceConfig struct {
	APIKey string `toml:"api_key"`
}

func (c OpenCodeConfig) Enabled() bool {
	return c.Go.APIKey != "" || c.Zen.APIKey != ""
}

type mekugiConfig struct {
	Providers    OpenCodeConfig    `toml:"providers"`
	ServiceTiers map[string]string `toml:"service_tiers"`
}

func loadMekugiConfig() (mekugiConfig, error) {
	var config mekugiConfig
	directory, err := os.UserConfigDir()
	if err != nil {
		// Without a configuration directory there can be no file to load.
		// Preserve environment-only setup and the existing startup diagnostics.
		config.Providers, err = openCodeEnvironment(OpenCodeConfig{})
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
		case "priority":
			config.ServiceTiers[model] = "fast"
		case "fast", "default", "auto", "flex":
		default:
			return mekugiConfig{}, errors.New("invalid service tier in Mekugi config.toml")
		}
	}
	config.Providers, err = openCodeEnvironment(config.Providers)
	return config, err
}

func openCodeEnvironment(config OpenCodeConfig) (OpenCodeConfig, error) {
	if value, ok := os.LookupEnv("OPENCODE_API_KEY"); ok {
		config.Go.APIKey = value
		config.Zen.APIKey = value
	}
	for _, entry := range []struct {
		env string
		key *string
	}{
		{"OPENCODE_GO_API_KEY", &config.Go.APIKey},
		{"OPENCODE_ZEN_API_KEY", &config.Zen.APIKey},
	} {
		if value, ok := os.LookupEnv(entry.env); ok {
			*entry.key = value
		}
		*entry.key = strings.TrimSpace(*entry.key)
		if strings.ContainsAny(*entry.key, "\r\n") {
			return OpenCodeConfig{}, errors.New("OpenCode API keys must not contain line breaks")
		}
	}
	return config, nil
}
