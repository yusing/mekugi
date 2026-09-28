//go:build journal_e2e

package router

import (
	"io"
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
	after := func(t *testing.T, _ io.Writer, await func(string), _ func(func(string) bool), screen *vt.Emulator) {
		t.Helper()
		await("echo SEG_NEVER · skipped")
		for _, want := range []string{"echo SEG_ONE", "false · exit 1"} {
			if !strings.Contains(screen.String(), want) {
				t.Fatalf("missing %q:\n%s", want, screen.String())
			}
		}
	}
	runAppServerPreviewWithEnvironment(t, provider, proxy, environment, nil, after)
}
