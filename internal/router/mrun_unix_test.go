//go:build unix

package router

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMRunCancellationDrainsDescendantPipes(t *testing.T) {
	testMRunCancellation(t, []string{"--max-tokens", "20", "--tail", "--", "sh", "-c", "sleep 30 & printf ready > ready; wait"})
}

func TestMRunLineCancellation(t *testing.T) {
	testMRunCancellation(t, []string{"-n", "20", "--", "sh", "-c", "printf ready > ready; exec yes"})
}

func testMRunCancellation(t *testing.T, arguments []string) {
	t.Helper()

	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	t.Chdir(directory)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	finished := make(chan int, 1)
	go func() {
		_, status := RunToolPluginWorker(ctx, registry.frontends["mrun"],
			arguments,
			nil, io.Discard, io.Discard)
		finished <- status
	}()
	ticker := time.Tick(10 * time.Millisecond)
	for {
		if _, err := os.Stat(filepath.Join(directory, "ready")); err == nil {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("command exited before ready: %v", err)
		case <-ctx.Done():
			t.Fatal("command did not become ready")
		case <-ticker:
		}
	}
	cancel()
	select {
	case status := <-finished:
		if status == 0 {
			t.Fatal("canceled command succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation left descendant output pipes open")
	}
}

func TestShellRunnerClosedInspectionPipes(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	linePath := filepath.Join(t.TempDir(), "line-rows.txt")
	if err := os.WriteFile(linePath, []byte(strings.Repeat(strings.Repeat("x", 80)+"\n", 13000)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep the token fixture larger than a one-megabyte pipe while reaching the
	// display limit in fewer rows. The worker still has to drain the whole file.
	tokenPath := filepath.Join(t.TempDir(), "token-rows.txt")
	if err := os.WriteFile(tokenPath, []byte(strings.Repeat(strings.Repeat("x", 4096)+"\n", 257)), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := map[string]string{
		"mcat_lines":  "mcat -n 20000 " + shellQuoteArgument(linePath),
		"mcat_tokens": "mcat --max-tokens 15500 " + shellQuoteArgument(tokenPath),
	}
	cases := []struct {
		command, mode string
	}{
		{"mcat_lines", "default"},
		{"mcat_lines", "pipefail"},
		{"mcat_lines", "errexit"},
		{"mcat_tokens", "pipefail"},
	}
	for _, tc := range cases {
		t.Run(tc.command+"/"+tc.mode, func(t *testing.T) {
			t.Parallel()
			command := commands[tc.command]
			mode := map[string]string{
				"default": "", "pipefail": "set -o pipefail\n", "errexit": "set -eo pipefail\n",
			}[tc.mode]
			stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
				mode+command+" | head -n 1 >/dev/null\nprintf 'AFTER:%s' \"$?\"", nil)
			if tc.mode == "errexit" {
				if stdout != "" || status != 141 || strings.Contains(stderr, "broken pipe") {
					t.Fatalf("%s %s: %q %q status=%d", mode, command, stdout, stderr, status)
				}
				return
			}
			want := "AFTER:0"
			if tc.mode != "default" {
				want = "AFTER:141"
			}
			if stdout != want || status != 0 || strings.Contains(stderr, "broken pipe") {
				t.Fatalf("%s %s: %q %q status=%d", mode, command, stdout, stderr, status)
			}
		})
	}
}
