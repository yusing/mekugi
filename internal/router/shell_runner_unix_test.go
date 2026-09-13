//go:build unix

package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

const (
	shellPTYHelperEnvironment   = "MEKUGI_SHELL_PTY_HELPER"
	shellPTYSnapshotEnvironment = "MEKUGI_SHELL_PTY_SNAPSHOT"
)

func TestShellRunnerExternalPipelineReadsPTY(t *testing.T) {
	if mode := os.Getenv(shellPTYHelperEnvironment); mode != "" {
		prefix := ""
		if mode == "hrun" {
			prefix = "hrun --max-tokens 100 --tail -- "
		}
		var stdout, stderr bytes.Buffer
		handled, exitCode := runAuthenticatedToolWorker(
			t.Context(), os.Getenv(shellPTYSnapshotEnvironment), "shell",
			[]string{"bash", `printf 'stream\n' | ` + prefix + `sh -c 'IFS= read -r stream; IFS= read -r terminal </dev/tty; printf "pty:%s:%s" "$stream" "$terminal"'`},
			os.Stdin, &stdout, &stderr,
		)
		if !handled {
			t.Fatal("shared snapshot did not identify the shell worker")
		}
		_, _ = fmt.Fprintf(os.Stdout, "MEKUGI_PTY_RESULT=%d|%s|%s\n", exitCode, stdout.String(), stderr.String())
		return
	}

	registry := sharedProxyTestRegistry(t)
	for _, mode := range []string{"direct", "hrun"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShellRunnerExternalPipelineReadsPTY$")
			command.Env = append(os.Environ(), shellPTYHelperEnvironment+"="+mode,
				shellPTYSnapshotEnvironment+"="+registry.SnapshotDir)
			terminal, err := pty.Start(command)
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			if _, err := io.WriteString(terminal, "hello\n"); err != nil {
				t.Fatal(err)
			}
			output, readErr := io.ReadAll(terminal)
			waitErr := command.Wait()
			if ctx.Err() != nil {
				t.Fatalf("PTY shell command did not finish: %v", ctx.Err())
			}
			if readErr != nil && !errors.Is(readErr, syscall.EIO) {
				t.Fatal(readErr)
			}
			if waitErr != nil {
				t.Fatalf("PTY helper failed: %v\n%s", waitErr, output)
			}
			if !strings.Contains(string(output), "MEKUGI_PTY_RESULT=0|pty:stream:hello|") {
				t.Fatalf("PTY shell output = %q", output)
			}
		})
	}
}
