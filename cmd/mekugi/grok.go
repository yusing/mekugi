package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/internal/router"
)

// Explicit invocation models win over Grok's default, which wins over the
// latest standard model. Codex's own configured default is not a Grok default.
func thirdPartyDefaultArgs(args []string, session router.Session) ([]string, error) {
	if router.HasModelOverride(args) {
		return args, nil
	}
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	model := ""
	if session.GrokEnabled {
		model = "grok-4.7"
	}
	if home, err := os.UserHomeDir(); err == nil && session.GrokEnabled {
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
		if !session.GrokUnprefixed {
			model = "grok:" + model
		}
	}
	if model == "" {
		model = session.OpenCode.DefaultModel()
		if model == "" {
			return nil, errors.New("authenticated third-party providers have no available models")
		}
	}
	return slices.Insert(slices.Clone(args), index, "-c", fmt.Sprintf("model=%q", model)), nil
}
