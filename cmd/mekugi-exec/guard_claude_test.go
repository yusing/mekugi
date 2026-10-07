package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/execsegment"
)

func TestNativeGuardEnvironmentReceipts(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("requires Bash")
	}
	t.Setenv("CLAUDE_CODE_SHELL", "/bin/bash")
	t.Setenv("CLAUDE_CODE_SHELL_PREFIX", "")
	prior, present := os.LookupEnv(execsegment.Guard)
	os.Unsetenv(execsegment.Guard)
	t.Cleanup(func() {
		if present {
			os.Setenv(execsegment.Guard, prior)
		} else {
			os.Unsetenv(execsegment.Guard)
		}
	})
	startup := filepath.Join(t.TempDir(), "caller startup with spaces")
	tracker := filepath.Join(filepath.Dir(startup), "exec-track.bash")
	for _, path := range []string{startup, tracker} {
		if err := os.WriteFile(path, []byte(":\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BASH_ENV", startup)
	receipts := t.TempDir()
	input := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"native/command"}`
	path := filepath.Join(receipts, fmt.Sprintf("%x", sha256.Sum256([]byte("native/command"))))
	if err := nativeGuardEnvironment(startup, receipts, strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "ok" {
		t.Fatalf("receipt=%q err=%v", data, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, key, value, want string }{
		{"native settings override", "BASH_ENV", "/dev/null", "settings replaced"},
		{"native shell override", "CLAUDE_CODE_SHELL", "/missing/bash", "usable Bash"},
		{"native shell prefix", "CLAUDE_CODE_SHELL_PREFIX", "env BASH_ENV=/dev/null", "shell prefix"},
		{"inherited disabled observer", execsegment.Guard, "1", "disables"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			err := nativeGuardEnvironment(startup, receipts, strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
			if data, readErr := os.ReadFile(path); readErr != nil || !strings.Contains(string(data), "deny: "+err.Error()) {
				t.Fatalf("denial receipt=%q err=%v", data, readErr)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := nativeGuardEnvironment(startup, filepath.Join(receipts, "absent"), strings.NewReader(input)); err == nil {
		t.Fatal("missing receipt directory admitted")
	}
	for _, resource := range []string{startup, tracker} {
		t.Run(filepath.Base(resource), func(t *testing.T) {
			if err := os.Remove(resource); err != nil {
				t.Fatal(err)
			}
			if err := nativeGuardEnvironment(startup, receipts, strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "resource unavailable") {
				t.Fatalf("missing resource admitted: %v", err)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "deny: native startup resource unavailable" {
				t.Fatalf("resource denial receipt=%q err=%v", data, err)
			}
			if err := os.WriteFile(resource, []byte(":\n"), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}
