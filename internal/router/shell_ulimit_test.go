//go:build linux || darwin

package router

import (
	"strconv"
	"strings"
	"testing"
)

func TestShellUlimitInspection(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, option := range []string{"-Sn", "-Hn"} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, "ulimit "+option, nil)
		if _, err := strconv.ParseUint(strings.TrimSpace(stdout), 10, 64); err != nil || status != 0 || stderr != "" {
			t.Fatalf("%s: (%q, %q, %d)", option, stdout, stderr, status)
		}
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, "ulimit -a", nil)
	if status != 0 || stderr != "" || !strings.Contains(stdout, "open files (-n)") {
		t.Fatalf("all: (%q, %q, %d)", stdout, stderr, status)
	}
	stdout, stderr, status = runShellWorkerTest(t, registry, "bash", nil, "ulimit -n 64", nil)
	if status != 2 || stdout != "" || !strings.Contains(stderr, "only inspection") {
		t.Fatalf("mutation: (%q, %q, %d)", stdout, stderr, status)
	}
}
