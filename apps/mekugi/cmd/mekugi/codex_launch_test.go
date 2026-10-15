package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWrapCodexSkipsThirdPartyCatalog(t *testing.T) {
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "MEKUGI_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("XAI_API_KEY", "grok-test")
	t.Setenv("OPENCODE_API_KEY", "shared-test")
	t.Setenv("OPENCODE_GO_API_KEY", "invalid\nunused")
	t.Setenv("OPENCODE_ZEN_API_KEY", "zen-test")
	directory := t.TempDir()
	record := filepath.Join(directory, "args.txt")
	t.Setenv("MEKUGI_TEST_CODEX_ARGS", record)
	stub := "#!/bin/sh\ncase \"$*\" in *\"debug models\"*) exit 91;; esac\nprintf '%s\\n' \"$@\" > \"$MEKUGI_TEST_CODEX_ARGS\"\n"
	if err := os.WriteFile(filepath.Join(directory, "codex"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	code, err := wrapCodex(ctx, nil, []string{"exec", "--profile", "work", "--ignore-user-config", "prompt"})
	if code != 0 || err != nil {
		t.Fatalf("Codex-only launch failed: code=%d error=%v", code, err)
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), `model_provider="openai"`) || !strings.Contains(string(args), `openai_base_url="http://127.0.0.1:`) || !strings.HasPrefix(string(args), "exec\n--profile\nwork\n--ignore-user-config\nprompt\n") {
		t.Fatalf("Codex launch lost host authentication or selectors: %s", args)
	}
	if strings.Contains(string(args), "model_catalog_json=") || strings.Contains(string(args), "model=\"grok") {
		t.Fatalf("Codex launch received third-party catalog/default overrides: %s", args)
	}
}
