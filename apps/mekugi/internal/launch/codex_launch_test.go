package launch

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
	stub := "#!/bin/sh\ncase \"$*\" in *\"debug models\"*) exit 91;; esac\nprintf '%s\\n' invocation >> \"$MEKUGI_TEST_CODEX_ARGS.calls\"\nprintf '%s\\n' \"$@\" > \"$MEKUGI_TEST_CODEX_ARGS\"\n"
	if err := os.WriteFile(filepath.Join(directory, "codex"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	code, err := wrapCodex(ctx, nil, []string{"exec", "--ignore-user-config", "prompt"})
	if code != 0 || err != nil {
		t.Fatalf("Codex-only launch failed: code=%d error=%v", code, err)
	}
	calls, err := os.ReadFile(record + ".calls")
	if err != nil || string(calls) != "invocation\n" {
		t.Fatalf("noninteractive command must execute once: calls=%q error=%v", calls, err)
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), `model_provider="openai"`) || !strings.Contains(string(args), `openai_base_url="http://127.0.0.1:`) || !strings.HasPrefix(string(args), "exec\n--ignore-user-config\nprompt\n") {
		t.Fatalf("Codex launch lost host authentication or selectors: %s", args)
	}
	if strings.Contains(string(args), "model_catalog_json=") || strings.Contains(string(args), "model=\"grok") {
		t.Fatalf("Codex launch received third-party catalog/default overrides: %s", args)
	}
}

func TestWrapProfileImportWithoutProviderRetainsCodexRoute(t *testing.T) {
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "MEKUGI_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("OPENCODE_GO_API_KEY", "invalid\nunused")
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
