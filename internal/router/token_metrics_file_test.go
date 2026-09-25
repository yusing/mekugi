package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenMetricsFileUsesStableThreadIdentity(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	report := tokenUsageReport{InputTokens: 12, UncachedInputTokens: 12, OutputTokens: 3}
	first := &mekugiProxy{}
	first.writeTokenMetrics("session/a", report)
	paths := first.tokenMetricPaths()
	if len(paths) != 1 || filepath.Dir(paths[0]) != directory || filepath.Ext(paths[0]) != ".md" {
		t.Fatalf("metric paths = %q", paths)
	}
	initial, err := os.ReadFile(paths[0])
	if err != nil || !strings.Contains(string(initial), "| Agent | Role | Model |") {
		t.Fatalf("initial markdown = %q, %v", initial, err)
	}
	second := &mekugiProxy{}
	report.InputTokens = 40
	second.writeTokenMetrics("session/a", report)
	if got := second.tokenMetricPaths(); len(got) != 1 || got[0] != paths[0] {
		t.Fatalf("resumed session path = %q, want %q", got, paths)
	}
	updated, err := os.ReadFile(paths[0])
	if err != nil || string(updated) == string(initial) {
		t.Fatalf("updated markdown = %q, %v", updated, err)
	}
	second.writeTokenMetrics("session/b", report)
	if got := second.tokenMetricPaths(); len(got) != 2 || got[0] == got[1] {
		t.Fatalf("distinct session paths = %q", got)
	}
}
