//go:build !unix

package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/shellsyntax"
)

func TestResetGuidanceUnavailablePlatform(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "POLICY.md")
	if err := os.WriteFile(path, []byte("Platform policy.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]any{"cmd": "cat POLICY.md"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal([]map[string]any{
		{"type": "function_call", "name": nativeExecCommandToolName, "call_id": "read", "arguments": string(arguments)},
		{"type": "function_call_output", "call_id": "read", "output": "read complete"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]jsonv1.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatal(err)
	}
	got := collectResetGuidance(t.Context(), items, "Read POLICY.md.", workspace, nil, nil)
	if got == nil || len(got.Commands) != 0 || got.Name != "" || got.Tool != "" {
		t.Fatalf("unsupported snapshot admitted guidance: %+v", got)
	}
	if !strings.Contains(got.Text, "Unavailable: cat "+shellsyntax.QuoteArgument(path)) ||
		!strings.Contains(got.Text, "link-safe guidance snapshots are unavailable on this platform") ||
		strings.Contains(got.Text, "Platform policy.") {
		t.Fatalf("missing platform-unavailable notice: %s", got.Text)
	}
}
