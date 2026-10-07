package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/internal/router"
)

func TestThirdPartyDefaultModelSelection(t *testing.T) {
	for _, tc := range []struct {
		name, config, want string
		args               []string
		unprefixed         bool
	}{
		{name: "latest", want: "grok-4.7", unprefixed: true},
		{name: "configured", config: "[models]\ndefault = 'grok-4.5'\n", want: "grok-4.5", unprefixed: true},
		{name: "empty", config: "[models]\ndefault = ''\n", want: "grok-4.7", unprefixed: true},
		{name: "unrelated", config: "[models]\nweb_search = 'other'\n", want: "grok-4.7", unprefixed: true},
		{name: "standalone", config: "[models]\ndefault = 'grok-4.5'\n", want: "grok:grok-4.5"},
		{name: "standalone_latest", want: "grok:grok-4.7"},
		{name: "literal_after_delimiter", args: []string{"exec", "--", "-m", "not-a-model"}, want: "grok-4.7", unprefixed: true},
		{name: "option_operand", args: []string{"exec", "--image", "-model.png"}, want: "grok-4.7", unprefixed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("HOME", directory)
			if tc.config != "" {
				writeGrokConfig(t, directory, tc.config)
			}
			args, err := thirdPartyDefaultArgs(tc.args, router.Session{GrokEnabled: true, GrokUnprefixed: tc.unprefixed, ThirdPartyOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			index := slices.Index(tc.args, "--")
			if index < 0 {
				index = len(tc.args)
			}
			var config struct{ Model string }
			if _, err := toml.Decode(args[index+1], &config); err != nil || config.Model != tc.want {
				t.Fatalf("selected model = %q, want %q: %v", config.Model, tc.want, err)
			}
			if !slices.Equal(args[:index], tc.args[:index]) || !slices.Equal(args[index+2:], tc.args[index:]) {
				t.Fatalf("original arguments changed: %q", args)
			}
		})
	}
}

func TestThirdPartyExplicitModelSkipsGrokConfig(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	writeGrokConfig(t, directory, "invalid = [\n")
	for _, args := range [][]string{
		{"-m", "grok-4.5"}, {"--model=grok-4.5"}, {"-mgrok-4.5"},
		{"-c", `model="grok-4.5"`}, {"--config=model='grok-4.5'"}, {"-cmodel='grok-4.5'"},
	} {
		got, err := thirdPartyDefaultArgs(args, router.Session{GrokEnabled: true, GrokUnprefixed: true})
		if err != nil || !slices.Equal(got, args) {
			t.Fatalf("explicit model changed: %q, %v", got, err)
		}
	}
	if _, err := thirdPartyDefaultArgs(nil, router.Session{GrokEnabled: true}); err == nil || !strings.Contains(err.Error(), "select a model with -m") {
		t.Fatalf("invalid default config was not actionable: %v", err)
	}
}

func writeGrokConfig(t *testing.T, directory, config string) {
	t.Helper()
	path := filepath.Join(directory, ".grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestThirdPartyOpenCodeDefault(t *testing.T) {
	for _, tc := range []struct {
		providers router.OpenCodeConfig
		prefix    string
	}{
		{router.OpenCodeConfig{Go: router.OpenCodeServiceConfig{APIKey: "test"}, Zen: router.OpenCodeServiceConfig{APIKey: "test"}}, "opencode-go:"},
		{router.OpenCodeConfig{Zen: router.OpenCodeServiceConfig{APIKey: "test"}}, "opencode-zen:"},
	} {
		args, err := thirdPartyDefaultArgs(nil, router.Session{ThirdPartyOnly: true, OpenCode: tc.providers})
		var config struct{ Model string }
		if err != nil {
			t.Fatal(err)
		}
		if _, err := toml.Decode(args[1], &config); err != nil || !strings.HasPrefix(config.Model, tc.prefix) {
			t.Fatalf("OpenCode default = %q, want provider %q: %v", config.Model, tc.prefix, err)
		}
	}
}

func TestCodexArgsThirdPartyAuthentication(t *testing.T) {
	args := codexArgs("http://127.0.0.1:12345/v1", []string{"exec", "hello"}, false, false, false)
	var overrides []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			overrides = append(overrides, args[i])
		}
	}
	var config struct {
		ModelProvider string `toml:"model_provider"`
		Providers     map[string]struct {
			Auth   bool   `toml:"requires_openai_auth"`
			EnvKey string `toml:"env_key"`
		} `toml:"model_providers"`
	}
	if _, err := toml.Decode(strings.Join(overrides, "\n"), &config); err != nil {
		t.Fatal(err)
	}
	provider, ok := config.Providers[config.ModelProvider]
	if !ok || provider.Auth || provider.EnvKey != "" {
		t.Fatalf("third-party launch depends on Codex/provider auth: %+v", provider)
	}
}

func TestThirdPartyResumeArgvPreservesMode(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want []string
	}{
		{"grok", []string{"mekugi", "--debug", "grok", "--yolo"}},
		{"third-party", []string{"mekugi", "--debug", "--yolo"}},
	} {
		got := appServerResumeArgv("mekugi", []string{"--debug", tc.mode}, []string{"resume", "old-thread", "--yolo"})
		if !slices.Equal(got, tc.want) {
			t.Fatalf("resume switched launch mode: %q, want %q", got, tc.want)
		}
	}
}

func TestThirdPartyDefaultsKeepUserResumeProvenance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, mode := range []string{"grok", "third-party"} {
		for _, explicit := range []bool{false, true} {
			userArgs := []string{"--yolo", "resume", "saved-thread"}
			if explicit {
				userArgs = append(userArgs, "-m", "grok-4.5")
			}
			resumeArgv := appServerResumeArgv("mekugi", []string{mode}, userArgs)
			args, _, _, err := appServerArgs(userArgs)
			if err != nil {
				t.Fatal(err)
			}
			args, err = thirdPartyDefaultArgs(args, router.Session{GrokEnabled: true, GrokUnprefixed: mode == "grok"})
			if err != nil || !router.HasModelOverride(args) {
				t.Fatalf("startup model missing: %q, %v", args, err)
			}
			if router.HasModelOverride(resumeArgv[1:]) != explicit {
				t.Fatalf("generated defaults leaked into user resume argv: %q", resumeArgv)
			}
		}
	}
}
