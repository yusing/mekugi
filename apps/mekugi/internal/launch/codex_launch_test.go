package launch

import (
	"os"
	"path/filepath"
	"testing"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
)

func TestNativeProfileImportWithoutProviderRetainsCodexRoute(t *testing.T) {
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "MEKUGI_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	profile := filepath.Join(os.Getenv("CODEX_HOME"), "work.config.toml")
	const body = "model=\"imported-literal\"\nmodel_context_window=123456\nmodel_auto_compact_token_limit=120000\n"
	if err := os.WriteFile(profile, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	imported, args, err := launchoptions.ImportProfile([]string{"-pwork", "-m", "explicit-literal"}, os.Environ())
	if err != nil || imported == nil {
		t.Fatalf("profile import: %v", err)
	}
	args, _, _, err = appServerArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	args = codexArgs("http://127.0.0.1:1234/v1", args, nil)
	values := launchoptions.ConfigOverrides(args)
	if values["model"] != "explicit-literal" || values["model_context_window"] != int64(123456) || values["model_auto_compact_token_limit"] != int64(120000) || values["model_provider"] != "openai" {
		t.Fatalf("native profile import changed route or options: %#v", values)
	}
	if after, _ := os.ReadFile(profile); string(after) != body {
		t.Fatal("wrapped launch modified source profile")
	}
}
