//go:build journal_e2e

package router

import (
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// Real Codex runs a command list through the tracking hook: its command item
// must match the helper's report by thread and script, so each segment gets
// its own row and status.
func TestAppServerExecTrackNativeCodex(t *testing.T) {
	shell := newExecTrackShell(t)
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	proxy.execTrack = shell.hub
	var environment []string
	for _, entry := range shell.env {
		if strings.HasPrefix(entry, "BASH_ENV=") {
			environment = append(environment, entry)
		}
	}
	provider := &toolFrontendCodexProvider{
		program:   `const result = await tools.exec_command({cmd:"echo SEG_ONE && false && echo SEG_NEVER"}); text(result.output);`,
		expected:  []string{"SEG_ONE"},
		finalText: "Recovered after a retry.",
	}
	after := func(t *testing.T, outer io.Writer, await func(string), _ func(func(string) bool), screen *vt.Emulator) {
		t.Helper()
		if _, err := io.WriteString(outer, "\x023"); err != nil {
			t.Fatal(err)
		}
		await("echo SEG_NEVER · skipped")
		for _, want := range []string{"echo SEG_ONE", "false · exit 1"} {
			if !strings.Contains(screen.String(), want) {
				t.Fatalf("missing %q:\n%s", want, screen.String())
			}
		}
		if _, err := io.WriteString(outer, "\x021"); err != nil {
			t.Fatal(err)
		}
	}
	runAppServerPreviewWith(t, provider, proxy, appServerPreview{environment: environment, afterPrompt: after, noJournal: true})
}

// Installed Codex must retain actual segment reports while its sandbox
// denies network access and writes to the session-private tracking directory.
func TestAppServerExecTrackNativeCodexSandbox(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("asserts the Linux sandbox boundary")
	}
	for _, sandbox := range []string{"read-only", "workspace-write"} {
		t.Run(sandbox, func(t *testing.T) {
			shell := newExecTrackShell(t)
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			proxy.execTrack = shell.hub
			var environment []string
			for _, entry := range shell.env {
				if strings.HasPrefix(entry, "BASH_ENV=") {
					environment = append(environment, entry)
				}
			}
			command := "{ echo > /dev/tcp/127.0.0.1/9; } 2>&1 | grep -o 'Operation not permitted'; echo SEG_ONE; echo SEG_TWO >&2; false && echo SEG_NEVER"
			provider := &toolFrontendCodexProvider{
				program:   `const result = await tools.exec_command({cmd:` + string(mustMarshalJSON(command)) + `}); text(result.output);`,
				expected:  []string{"Operation not permitted", "SEG_ONE", "SEG_TWO"},
				finalText: "Recovered after a retry.",
			}
			after := func(t *testing.T, outer io.Writer, await func(string), _ func(func(string) bool), screen *vt.Emulator) {
				t.Helper()
				if _, err := io.WriteString(outer, "\x023"); err != nil {
					t.Fatal(err)
				}
				await("echo SEG_NEVER · skipped")
				for _, want := range []string{"echo SEG_ONE", "echo SEG_TWO", "false · exit 1", "┆ SEG_ONE", "┆ SEG_TWO"} {
					if !strings.Contains(screen.String(), want) {
						t.Fatalf("missing segment evidence %q:\n%s", want, screen.String())
					}
				}
				if _, err := io.WriteString(outer, "\x021"); err != nil {
					t.Fatal(err)
				}
			}
			runAppServerPreviewWith(t, provider, proxy, appServerPreview{
				environment: environment,
				codexArgs:   []string{"-c", `sandbox_mode="` + sandbox + `"`, "-c", `approval_policy="on-request"`},
				approvals:   true,
				noJournal:   true,
				afterPrompt: after,
			})
			provider.mu.Lock()
			defer provider.mu.Unlock()
			if !provider.resultSeen {
				t.Fatalf("command output = %s", provider.output)
			}
		})
	}
}
