package router

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

type serviceTierSettings struct {
	configured map[string]string
	choices    sync.Map // [thread, routed model] -> confirmed tier
}

func serviceTierModel(model string) string {
	if model == "gpt-5.6-terra" {
		return "gpt-6-sol"
	}
	return model
}

func (s *serviceTierSettings) choice(model, thread string) (string, bool) {
	if s != nil {
		if tier, ok := s.choices.Load([2]string{thread, serviceTierModel(model)}); ok {
			return tier.(string), true
		}
	}
	return "", false
}

// effectiveServiceTier follows request model selection and leaves host settings
// untouched. The legacy Terra selection routes to Sol before tier lookup.
func effectiveServiceTier(model, requested string, settings *serviceTierSettings, thread string) string {
	model = serviceTierModel(model)
	if settings != nil {
		if tier, ok := settings.choice(model, thread); ok {
			requested = tier
		} else if tier := settings.configured[model]; tier != "" {
			requested = tier
		}
	}
	if requested == "fast" {
		return "priority"
	}
	return requested
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
		case "fast":
			config.ServiceTiers[model] = "priority"
		case "priority", "default", "auto", "flex":
		default:
			return mekugiConfig{}, errors.New("invalid service tier in Mekugi config.toml")
		}
	}
	if !withProviders {
		config.Providers = OpenCodeConfig{}
		return config, nil
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
