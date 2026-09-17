package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/yusing/mekugi/internal/router"
)

func TestPrepareOpenCodeCatalog(t *testing.T) {
	executable := catalogTestExecutable(t)
	config := router.OpenCodeConfig{
		Go:  router.OpenCodeServiceConfig{APIKey: "go-private"},
		Zen: router.OpenCodeServiceConfig{APIKey: "zen-private"},
	}
	directory, path, err := prepareProviderCatalog(t.Context(), executable, "http://127.0.0.1:12345/v1", nil, false, config)
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
