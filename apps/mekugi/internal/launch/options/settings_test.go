package options

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeConfig(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	// os.UserConfigDir ignores XDG_CONFIG_HOME on some platforms.
	actual, err := os.UserConfigDir()
	if err != nil || actual != directory {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	t.Setenv("OPENCODE_API_KEY", "")
	if err := os.Unsetenv("OPENCODE_API_KEY"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_GO_API_KEY", "")
	t.Setenv("OPENCODE_ZEN_API_KEY", "")
	config, err := LoadSettings(true)
	if err != nil || config.Providers.Enabled() {
		t.Fatalf("missing config: %v", err)
	}
	if err := os.Mkdir(filepath.Join(directory, "mekugi"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "mekugi", "config.toml")
	if err := os.WriteFile(path, []byte("[providers.opencode_go]\napi_key = 'config-go'\n[providers.opencode_zen]\napi_key = 'config-zen'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An explicitly empty environment key disables a configured provider.
	config, err = LoadSettings(true)
	if err != nil || config.Providers.Enabled() {
		t.Fatalf("empty override: %v", err)
	}
	if err := os.Unsetenv("OPENCODE_GO_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("OPENCODE_ZEN_API_KEY"); err != nil {
		t.Fatal(err)
	}
	config, err = LoadSettings(true)
	if err != nil || config.Providers.Go.APIKey != "config-go" || config.Providers.Zen.APIKey != "config-zen" {
		t.Fatalf("config not loaded: %v", err)
	}
	t.Setenv("OPENCODE_GO_API_KEY", " env-go ")
	config, err = LoadSettings(true)
	if err != nil || config.Providers.Go.APIKey != "env-go" || config.Providers.Zen.APIKey != "config-zen" {
		t.Fatalf("provider isolation/precedence: %v", err)
	}
	t.Setenv("OPENCODE_API_KEY", "shared")
	config, err = LoadSettings(true)
	if err != nil || config.Providers.Go.APIKey != "env-go" || config.Providers.Zen.APIKey != "shared" {
		t.Fatalf("shared key precedence: %v", err)
	}
	t.Setenv("OPENCODE_ZEN_API_KEY", "")
	config, err = LoadSettings(true)
	if err != nil || config.Providers.Go.APIKey != "env-go" || config.Providers.Zen.APIKey != "" {
		t.Fatalf("service-specific disable after shared key: %v", err)
	}
	for _, body := range []string{
		"[providers.opencode_go]\napi_key = 'private-secret",
		"[providers.opencode_go]\napi_keey = 'private-secret'\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSettings(true); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("unsanitized config error: %v", err)
		}
	}
}

func TestServiceTierConfig(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	if actual, _ := os.UserConfigDir(); actual != directory {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("OPENCODE_GO_API_KEY", "")
	t.Setenv("OPENCODE_ZEN_API_KEY", "")
	if err := os.Mkdir(filepath.Join(directory, "mekugi"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ value, want string }{
		{"fast", "priority"}, {"priority", "priority"}, {"default", "default"},
		{"auto", "auto"}, {"flex", "flex"}, {"", ""}, {"invalid", ""},
	} {
		t.Run(tc.value, func(t *testing.T) {
			body := fmt.Sprintf("[service_tiers]\n\"gpt-6-astra\" = %q\n", tc.value)
			if err := os.WriteFile(filepath.Join(directory, "mekugi", "config.toml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			config, err := LoadSettings(false)
			if tc.want == "" {
				if err == nil {
					t.Fatal("invalid tier accepted")
				}
				return
			}
			if err != nil || config.ServiceTiers["gpt-6-astra"] != tc.want {
				t.Fatalf("config=%v err=%v", config.ServiceTiers, err)
			}
		})
	}
}
