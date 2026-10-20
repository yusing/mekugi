package execscope

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestGoFixPackageScopeAcceptsManySources(t *testing.T) {
	workspace := t.TempDir()
	pkg := filepath.Join(workspace, "pkg")
	for i := range 512 {
		writeTestFile(t, filepath.Join(pkg, fmt.Sprintf("source-%03d.go", i)), "package pkg\n")
	}
	result := execGoPackageScope(execProviderInput{identity: "go", args: []string{"fix", "./pkg"}, cwd: workspace, deadline: time.Now().Add(5 * time.Second)})
	if len(result.scope) != 1 || len(result.scope[0].Operands) != 512 || !result.open || result.reason != "" {
		t.Fatalf("go fix package source scope: groups=%d open=%v reason=%q", len(result.scope), result.open, result.reason)
	}
}
