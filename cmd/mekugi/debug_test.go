package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/router"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotDebugHandoffAfterCodexExit(t *testing.T) {
	fixtures, err := filepath.Abs("testdata/snapshots")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, exit string
		overrides  bool
		relative   bool
	}{
		{name: "success", exit: "0"},
		{name: "failure", exit: "23"},
		{name: "overrides", exit: "0", overrides: true},
		{name: "relative-overrides", exit: "0", overrides: true, relative: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "debug space's $(not-a-command)")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("TMPDIR", directory)
			t.Setenv("MEKUGI_AX_OUTPUT", "")
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			if err := os.WriteFile(filepath.Join(directory, "codex"), []byte("#!/bin/sh\necho codex-finished >&2\nexit "+test.exit+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			output, err := os.CreateTemp(directory, "stderr-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = output.Close() })
			old := os.Stderr
			os.Stderr = output
			defer func() { os.Stderr = old }()
			args := []string{"--debug", "--mode", "passthrough"}
			if test.overrides {
				capture, reads := filepath.Join(directory, "capture.jsonl"), filepath.Join(directory, "reads.jsonl")
				if test.relative {
					t.Chdir(directory)
					capture = "capture.jsonl"
				}
				args = append(args, "--capture-output", capture)
				t.Setenv("MEKUGI_AX_OUTPUT", reads)
			}
			code, err := wrapCodex(t.Context(), args, nil)
			if err != nil || (test.exit == "0" && code != 0) || (test.exit == "23" && code != 23) {
				t.Fatalf("wrap exit: %d, %v", code, err)
			}
			data, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			before, after, ok := strings.Cut(string(data), "codex-finished\n")
			if !ok || strings.Contains(before, "To diagnose this session") || strings.Count(after, "To diagnose this session") != 1 {
				t.Fatalf("debug paths did not print only after child exit: %q", data)
			}
			lines := strings.Split(strings.TrimSpace(after), "\n")
			if len(lines) != 2 {
				t.Fatalf("diagnostic handoff is not one command: %q", after)
			}
			// Parse the printed shell command, then invoke the real diagnostic entry point.
			reader := `mekugi() { printf '%s\n' "$@"; }; `
			resolved, err := exec.CommandContext(t.Context(), "sh", "-c", reader+strings.TrimSpace(lines[1])).CombinedOutput()
			if err != nil {
				t.Fatalf("handoff command failed: %v: %s", err, resolved)
			}
			argv := strings.Split(strings.TrimSpace(string(resolved)), "\n")
			if len(argv) != 5 || argv[0] != "inspect-session" || argv[1] != "--debug-dir" || argv[3] != "--field" || argv[4] != "diagnostic" {
				t.Fatalf("diagnostic handoff is not a session inspection command: %q", resolved)
			}
			var diagnosticErrors bytes.Buffer
			if code := router.RunSessionInspection(t.Context(), argv[1:], io.Discard, &diagnosticErrors); code != 0 {
				t.Fatalf("printed diagnostic command failed: %d: %s", code, &diagnosticErrors)
			}
			normalized := strings.ReplaceAll(after, shellsyntax.Quote(argv[2]), "'/tmp/mekugi-debug-session'")
			uisnapshot.Assert(t, filepath.Join(fixtures, "debug-handoff.txt"), normalized)
		})
	}
}

// Exercise the real handoff tests in a fresh package invocation with ambient
// retained state. Neither generated bundles nor retention may touch that state.
func TestDebugHandoffTestsIsolateAmbientState(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(state, "mekugi", "debug")
	oldBundle := filepath.Join(root, "mekugi-debug-existing")
	if err := os.MkdirAll(oldBundle, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(oldBundle, ".mekugi-debug-v1.lock")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-15 * 24 * time.Hour)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestUISnapshotDebugHandoffAfterCodexExit$")
	command.Env = append(os.Environ(), "XDG_STATE_HOME="+state, "MEKUGI_LAUNCHER_TEST_STATE_ISOLATED=")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("handoff subprocess: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(oldBundle) {
		t.Fatalf("ambient debug state changed: %v, %v", entries, err)
	}
	info, err := os.Stat(marker)
	if err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("ambient retained bundle changed: %v, %v", info, err)
	}
}
