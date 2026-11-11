package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testNativeModelCatalog = `{"models":[{"slug":"gpt-5.6-sol","tool_mode":"code_mode","multi_agent_version":"v2","shell_type":"unified_exec","apply_patch_tool_type":"freeform","model_messages":{"instructions_template":"native instructions"}}]}`

func TestBundledModelCatalog(t *testing.T) {
	for _, mode := range []string{"ok", "missing", "oversized", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			content := testNativeModelCatalog
			if mode == "oversized" {
				content = strings.Repeat(" ", (8<<20)+1)
			}
			if mode != "missing" {
				if err := os.WriteFile(filepath.Join(directory, "mekugi-models.json"), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			body, err := bundledModelCatalog(ctx, filepath.Join(directory, "mekugi-app-server"))
			if mode == "ok" {
				if err != nil || string(body) != testNativeModelCatalog {
					t.Fatalf("catalog: %s %v", body, err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected catalog error")
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}
