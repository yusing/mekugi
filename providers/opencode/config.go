package opencode

import (
	"errors"
	"os"
	"strings"
)

// Source: internal/router/opencode_config.go:9:46@[18475aaf] service configuration.
// Config enables each independently authenticated service when its key
// is nonempty. It is invocation-local; loading never writes user configuration.
type Config struct {
	Catalog *Catalog      `toml:"-"`
	Go      ServiceConfig `toml:"opencode_go"`
	Zen     ServiceConfig `toml:"opencode_zen"`
}

type ServiceConfig struct {
	APIKey string `toml:"api_key"`
}

func (c Config) Enabled() bool {
	return c.Go.APIKey != "" || c.Zen.APIKey != ""
}

func Environment(config Config) (Config, error) {
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
			return Config{}, errors.New("OpenCode API keys must not contain line breaks")
		}
	}
	return config, nil
}

// Source: apps/mekugi/internal/router/opencode.go:7:9@[8877886e].
// IsModel recognizes OpenCode namespaces, including unsupported model IDs.
func IsModel(model string) bool {
	return strings.HasPrefix(model, "opencode-go:") || strings.HasPrefix(model, "opencode-zen:")
}
