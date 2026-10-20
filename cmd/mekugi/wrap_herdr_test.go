package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func TestHerdrWrapperHint(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_HERDR_HINT") == "1" {
		if err := exposeHerdrCodex(); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("HERDR_ENV") == "1" && os.Getenv("HERDR_AGENT") != "codex" {
			t.Fatal("missing Codex hint")
		}
		if runtime.GOOS == "linux" && os.Getenv("HERDR_ENV") == "1" {
			environ, err := os.ReadFile("/proc/self/environ")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(environ, []byte("HERDR_AGENT=codex\x00")) {
				t.Fatal("hint not visible to process detector")
			}
		}
		fmt.Printf("wrapper-pid=%d hint=%s\n", os.Getpid(), os.Getenv("HERDR_AGENT"))
		return
	}
	for _, tc := range []struct{ name, managed, hint, want string }{
		{"managed", "1", "", "codex"},
		{"existing", "1", "codex", "codex"},
		{"inherited other agent", "1", "claude", "codex"},
		{"outside Herdr", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHerdrWrapperHint$")
			cmd.Env = append(os.Environ(), "MEKUGI_TEST_HERDR_HINT=1", "HERDR_ENV="+tc.managed, "HERDR_AGENT="+tc.hint)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("wrapper hint: %v: %s", err, out)
			}
			want := fmt.Sprintf("wrapper-pid=%d hint=%s\n", cmd.Process.Pid, tc.want)
			if !bytes.Contains(out, []byte(want)) {
				t.Fatalf("PID or hint changed: %q, want %q", out, want)
			}
		})
	}
}
