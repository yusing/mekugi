package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
