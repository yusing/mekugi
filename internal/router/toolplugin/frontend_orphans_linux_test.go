//go:build linux

package toolplugin

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFrontendRetiresResolverOrphansWithoutDetachedGroup(t *testing.T) {
	if os.Getenv("MEKUGI_FRONTEND_ORPHAN_HELPER") == "1" {
		ctx, err := EnableFrontendOrphanCleanup(WithHostProcessGroup(t.Context()))
		if err != nil {
			t.Fatal(err)
		}
		var response executionResponse
		if err := invoke(ctx, os.Getenv("MEKUGI_FRONTEND_NODE"), os.Getenv("MEKUGI_FRONTEND_HOST"),
			"", "", nil, 1024, !hostProcessGroupOwned(ctx), nil, nil, map[string]any{}, &response); err != nil {
			t.Fatal(err)
		}
		return
	}
	node, err := resolveNodeRuntime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "orphan.pid")
	hostPath := filepath.Join(directory, "host.mjs")
	script := `import {spawn} from "node:child_process";
import {writeFileSync} from "node:fs";
const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {stdio: "ignore"});
writeFileSync(` + strconv.Quote(pidPath) + `, String(child.pid));
child.unref();
process.stdout.write('{"stdout":"","stderr":"","exitCode":0,"terminationReason":"resolver_cleanup"}\n');
`
	if err := os.WriteFile(hostPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestFrontendRetiresResolverOrphansWithoutDetachedGroup$")
	command.Env = append(os.Environ(), "MEKUGI_FRONTEND_ORPHAN_HELPER=1", "MEKUGI_FRONTEND_NODE="+node, "MEKUGI_FRONTEND_HOST="+hostPath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("frontend helper: %v: %s", err, output)
	}
	encoded, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("frontend resolver orphan %d survived cleanup", pid)
}
