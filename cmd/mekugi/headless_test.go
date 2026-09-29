package main

import (
	"bufio"
	"bytes"
	"context"
	json "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

func TestHeadlessWrapRedirectedEntryPoint(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("MEKUGI_AUTO_WRAP_PROCESS", "1")
	t.Setenv("MEKUGI_AUTO_WRAP_HEADLESS", "1")
	t.Setenv("MEKUGI_AUTO_WRAP_FAILURE", "0")
	t.Setenv("MEKUGI_AUTO_WRAP_RPC_MARKER", filepath.Join(dir, "rpc"))
	t.Setenv("MEKUGI_AUTO_WRAP_WORKSPACE", workspace)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run=^TestAutoWrapProcess$ -- codex \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAutoWrapProcess$", "--", "router")
	cmd.Dir, cmd.Stdin = workspace, strings.NewReader("Complete a small task.")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("headless: %v\n%s", err, &stderr)
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var last string
	for scanner.Scan() {
		var event appserver.Message
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("non-JSON stdout: %q: %v", scanner.Text(), err)
		}
		last = event.Method
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if last != "mekugi/headless/completed" {
		t.Fatalf("missing terminal event: %s", output)
	}
	methods, err := os.ReadFile(filepath.Join(dir, "rpc"))
	if err != nil || string(methods) != "initialize\ninitialized\nthread/start\nturn/start\n" {
		t.Fatalf("RPC sequence: %s (%v)", methods, err)
	}
}

func TestHeadlessRejectsUnsupportedAuthorizationAndResume(t *testing.T) {
	for _, args := range [][]string{{"headless"}, {"headless", "--yolo", "resume", "thread"}, {"headless", "--yolo", "prompt"}} {
		code, err := wrapCodex(t.Context(), nil, args)
		if code != 2 || err == nil {
			t.Fatalf("%v: code=%d err=%v", args, code, err)
		}
	}
}
