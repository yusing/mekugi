package launch

import (
	"bytes"
	"os"
	"testing"

	"github.com/yusing/mekugi/providers/opencode"

	"github.com/yusing/mekugi/apps/mekugi/internal/router"
)

func TestPrepareOpenCodeCatalog(t *testing.T) {
	executable := catalogTestExecutable(t)
	config := opencode.Config{
		Go:  opencode.ServiceConfig{APIKey: "go-private"},
		Zen: opencode.ServiceConfig{APIKey: "zen-private"},
	}
	directory, path, err := prepareProviderCatalog(t.Context(), executable, nil, router.Session{ThirdPartyOnly: true, OpenCode: config})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"opencode-go:", "opencode-zen:"} {
		if !bytes.Contains(body, []byte(prefix)) {
			t.Fatalf("missing provider %q", prefix)
		}
	}
	for _, forbidden := range []string{"go-private", "zen-private", "grok:grok-4.6"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("unexpected catalog content: %q", forbidden)
		}
	}
}
