package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrapProfileImportWithoutProviderRetainsCodexRoute(t *testing.T) {
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "MEKUGI_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	profile := filepath.Join(os.Getenv("CODEX_HOME"), "work.config.toml")
	const body = "model=\"imported-literal\"\nmodel_context_window=123456\nmodel_auto_compact_token_limit=120000\n"
	if err := os.WriteFile(profile, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	record := filepath.Join(directory, "args")
	t.Setenv("MEKUGI_TEST_CODEX_ARGS", record)
	if err := os.WriteFile(filepath.Join(directory, "codex"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MEKUGI_TEST_CODEX_ARGS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, err := wrapCodex(t.Context(), nil, []string{"-pwork", "exec", "-m", "explicit-literal", "--", "--profile=literal prompt"})
	if code != 0 || err != nil {
		t.Fatalf("wrapped profile: %d %v", code, err)
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(args)
	if strings.Contains(wire, "-pwork") || !strings.Contains(wire, "model_context_window=123456") || !strings.Contains(wire, `model_provider="openai"`) || !strings.HasSuffix(wire, "--\n--profile=literal prompt\n") {
		t.Fatalf("wrapped import changed route or operands: %s", wire)
	}
	if after, _ := os.ReadFile(profile); string(after) != body {
		t.Fatal("wrapped launch modified source profile")
	}
}
