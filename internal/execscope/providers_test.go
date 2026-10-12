package execscope

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecProviderDeadlineAndDenoPermissionSet(t *testing.T) {
	root := t.TempDir()
	plan := classifyExecShellSource(`python3 -c 'open("a","w")'`, root, "bash", time.Now().Add(-time.Second), 0, nil, false)
	if plan.Class != Opaque || !strings.Contains(plan.Reason, "deadline") {
		t.Fatalf("expired provider: %+v", plan)
	}
	writeTestFile(t, filepath.Join(root, "deno.json"), `{"permissions":{"default":{"write":["src"]},"spawn":{"write":["src"],"run":true}}}`)
	paths, open := execDenoPermissionSet(root, "default")
	if open || len(paths) != 1 || paths[0] != filepath.Join(root, "src") {
		t.Fatalf("permission set: %v %v", paths, open)
	}
	if _, open := execDenoPermissionSet(root, "spawn"); !open {
		t.Fatal("subprocess permissions closed the scope")
	}
}
