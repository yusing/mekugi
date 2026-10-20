package router

import (
	"errors"
	"os"
	"strings"
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
