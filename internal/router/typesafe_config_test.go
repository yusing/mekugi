package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypeSafeConfigPrecedence(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	if actual, _ := os.UserConfigDir(); actual != directory {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	t.Setenv("TYPESAFE_API_KEY", " env-key ")
	config, err := loadMekugiConfig()
	if err != nil || config.typeSafeAPIKey() != "env-key" {
		t.Fatalf("environment fallback: key=%q err=%v", config.typeSafeAPIKey(), err)
	}
	configDir := filepath.Join(directory, "mekugi")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "config.toml")
	for _, tc := range []struct {
		name, body, want string
	}{
		{"absent", "[service_tiers]\n", "env-key"},
		{"file", "[typesafe]\napi_key = ' file-key '\n", "file-key"},
		{"empty", "[typesafe]\napi_key = ''\n", ""},
		{"whitespace", "[typesafe]\napi_key = '  '\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			config, err := loadMekugiConfig()
			if err != nil || config.typeSafeAPIKey() != tc.want {
				t.Fatalf("key=%q, want %q, err=%v", config.typeSafeAPIKey(), tc.want, err)
			}
		})
	}
	for _, body := range []string{
		"[typesafe]\napi_key = 12\n",
		"[typesafe]\napi_keey = 'private-secret'\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadMekugiConfig(); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("invalid config accepted or secret leaked: %v", err)
		}
	}
}

func TestRunSessionTypeSafeConfigOverridesEnvironment(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	if actual, _ := os.UserConfigDir(); actual != directory {
		t.Skip("platform does not use XDG_CONFIG_HOME")
	}
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	if err := os.Mkdir(filepath.Join(directory, "mekugi"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "mekugi", "config.toml")
	if err := os.WriteFile(path, []byte("[typesafe]\napi_key = ''\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunSession(t.Context(), []string{"--explore-filter"}, nil, func(Session) {
		t.Error("ready with explicitly disabled TypeSafe key")
	}, nil); err == nil || !strings.Contains(err.Error(), "TypeSafe API key") {
		t.Fatalf("explicit filter accepted without effective key: %v", err)
	}
	if err := os.WriteFile(path, []byte("[typesafe]\napi_key = 'file-key'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	called := false
	err := RunSession(ctx, []string{"--explore-filter", "--mentor-handoff=false"}, nil, func(Session) {
		called = true
		cancel()
	}, nil)
	if err != nil || !called {
		t.Fatalf("configured filter did not start: ready=%v err=%v", called, err)
	}
}
