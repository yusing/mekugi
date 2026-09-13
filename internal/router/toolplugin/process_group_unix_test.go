//go:build unix

package toolplugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInvokeCancellationTerminatesPluginProcessGroup(t *testing.T) {
	node, err := resolveNodeRuntime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "child.pid")
	hostPath := filepath.Join(directory, "host.mjs")
	script := `import {spawn} from "node:child_process";
import {writeFileSync} from "node:fs";
const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {stdio: "ignore"});
writeFileSync(` + strconv.Quote(pidPath) + `, String(child.pid));
setInterval(() => {}, 1000);
`
	if err := os.WriteFile(hostPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		var response map[string]any
		result <- invoke(ctx, node, hostPath, "", "", nil, 1024, nil, nil, map[string]any{}, &response)
	}()

	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		encoded, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(encoded)))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("plugin child process did not start")
	}

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("invoke cancellation error = %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("plugin child process %d survived cancellation", childPID)
}

func TestInvokeCancellationAfterHostExit(t *testing.T) {
	t.Parallel()
	node, err := resolveNodeRuntime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "orphan.pid")
	hostPath := filepath.Join(directory, "host.mjs")
	// The child publishes readiness only after it is reparented. Both output
	// descriptors remain inherited, so host exit alone cannot finish capture.
	script := `import {spawn} from "node:child_process";
const child = spawn(process.execPath, ["-e", ` + strconv.Quote(`
const {writeFileSync} = require("node:fs");
const timer = setInterval(() => {
  if (process.ppid !== Number(process.argv[1])) {
    writeFileSync(process.argv[2], String(process.pid));
    clearInterval(timer);
    setInterval(() => {}, 1000);
  }
}, 5);
`) + `, String(process.pid), ` + strconv.Quote(pidPath) + `], {stdio: "inherit"});
process.exit(0);
`
	if err := os.WriteFile(hostPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		var response map[string]any
		result <- invoke(ctx, node, hostPath, "", "", nil, 1024, nil, nil, map[string]any{}, &response)
	}()
	var childPID int
	t.Cleanup(func() {
		if childPID != 0 {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if encoded, err := os.ReadFile(pidPath); err == nil {
			childPID, _ = strconv.Atoi(string(encoded))
			if childPID != 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("host did not exit and leave its output descriptors with the child")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("invoke cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation stopped owning the child when the host exited")
	}
}
