package launch

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerArgumentsCannotSelectMCPBridge(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A pinned executable shape selects worker dispatch, but its missing
	// manifest must still fail authentication for every operand spelling.
	directory := filepath.Join(t.TempDir(), "mekugi-tools-test-"+strings.Repeat("0", 64))
	if err := os.MkdirAll(filepath.Join(directory, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	frontend := filepath.Join(directory, "bin", "mcat")
	if err := os.Symlink(executable, frontend); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, invoked string
		args          []string
		code          int
		diagnostic    string
	}{
		{"ordinary", frontend, []string{"ordinary-file"}, 1, "open tool worker manifest"},
		{"bridge-name", frontend, []string{"mcp-bridge"}, 1, "open tool worker manifest"},
		{"bridge-and-operand", frontend, []string{"mcp-bridge", filepath.Join(directory, "missing.sock")}, 1, "open tool worker manifest"},
		{"explicit-bridge", executable, []string{"mcp-bridge"}, 2, "mcp-bridge requires one socket path"},
		{"web-help", executable, []string{"web", "--help"}, 0, "HTTP listen address for browser assets"},
		{"worker-web", frontend, []string{"web", "--help"}, 1, "open tool worker manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr, err := os.CreateTemp(t.TempDir(), "stderr-")
			if err != nil {
				t.Fatal(err)
			}
			previousArgs, previousStderr := os.Args, os.Stderr
			os.Args, os.Stderr = append([]string{tc.invoked}, tc.args...), stderr
			t.Cleanup(func() { os.Args, os.Stderr = previousArgs, previousStderr; stderr.Close() })
			code := Run()
			if _, err := stderr.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			message, err := io.ReadAll(stderr)
			if err != nil {
				t.Fatal(err)
			}
			if code != tc.code || !strings.Contains(string(message), tc.diagnostic) {
				t.Fatalf("dispatch = %d, %q; want %d, %q", code, message, tc.code, tc.diagnostic)
			}
		})
	}
}
