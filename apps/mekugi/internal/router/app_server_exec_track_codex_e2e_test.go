//go:build codex_e2e

package router

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/commandsegments/segments"

	"github.com/yusing/mekugi/backends/codex/shellhook"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Dash has no BASH_ENV or DEBUG trap. The installed host must apply the native
// hook and expose the same streaming, timing and per-command output as Bash.
func TestAppServerExecTrackNativeCodexDash(t *testing.T) {
	resolved, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil || filepath.Base(resolved) != "dash" {
		t.Skip("requires dash-backed /bin/sh")
	}
	shell := newExecTrackShell(t)
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	tracker := filepath.Join(shell.root, "exec-track.sh")
	if err := os.WriteFile(tracker, []byte(shellinstrumentation.ShTracker(helper, shell.transport.Endpoints().Requests, shell.transport.Endpoints().Directory)), 0o600); err != nil {
		t.Fatal(err)
	}
	hook, state, _, err := shellhook.Config(helper, "", "", tracker, shellhook.Invocation{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	integration := testAppServerIntegration(proxy)
	integration.execTrack = shell.hub.Observer
	gate := filepath.Join(shell.root, "continue")
	command := "echo DASH_ONE; { sleep 1; while [ ! -f " + quoteShellWord(gate) + " ]; do sleep 0.05; done; }; echo DASH_TWO >&2; false && echo DASH_NEVER"
	provider := &toolFrontendCodexProvider{
		program:  `const result = await tools.exec_command({cmd:` + string(responses.MustMarshalJSON(command)) + `,shell:"/bin/sh",login:false}); text(result.output);`,
		expected: []string{"DASH_ONE", "DASH_TWO"}, finalText: "Recovered after a retry.",
	}
	var tracked *segments.Subscription
	during := func(t *testing.T, outer io.Writer, await func(string), _ func(func(string) bool), _ *vt.Emulator) {
		t.Helper()
		if _, err := io.WriteString(outer, "\x023"); err != nil {
			t.Fatal(err)
		}
		await("┆ DASH_ONE")
		proxy.execWindows.mu.Lock()
		for _, window := range proxy.execWindows.windows {
			if window.tracking == nil {
				continue
			}
			for _, match := range window.tracking.State().Matches {
				if match.Source == command && match.Report != nil && !match.Report.Ended {
					tracked = window.tracking.Subscription
				}
			}
		}
		proxy.execWindows.mu.Unlock()
		if tracked == nil {
			t.Fatal("streamed output has no live dash observation")
		}
		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(outer, "\x021"); err != nil {
			t.Fatal(err)
		}
	}
	after := func(t *testing.T, outer io.Writer, await func(string), awaitFrame func(func(string) bool), screen *vt.Emulator) {
		t.Helper()
		if _, err := io.WriteString(outer, "\x023"); err != nil {
			t.Fatal(err)
		}
		await("echo DASH_NEVER · skipped")
		for _, want := range []string{"echo DASH_ONE", "echo DASH_TWO", "false · exit 1", "┆ DASH_ONE", "┆ DASH_TWO"} {
			if !strings.Contains(screen.String(), want) {
				t.Fatalf("missing dash segment %q:\n%s", want, screen.String())
			}
		}
		for _, output := range []string{"DASH_ONE", "DASH_TWO"} {
			row := -1
			for y, line := range strings.Split(screen.String(), "\n") {
				if strings.Contains(line, "echo "+output) {
					row = y
					break
				}
			}
			if row < 0 {
				t.Fatalf("missing click target for %s", output)
			}
			if _, err := fmt.Fprintf(outer, "\x1b[<0;10;%dM\x1b[<0;10;%dm", row+1, row+1); err != nil {
				t.Fatal(err)
			}
			awaitFrame(func(frame string) bool { return strings.Contains(frame, "y copy") })
			// Read only the dialog, excluding the faded Activity underneath.
			var body []string
			inside := false
			for _, line := range strings.Split(screen.String(), "\n") {
				chars := []rune(line)
				if len(chars) < 95 {
					continue
				}
				modal := string(chars[5:95])
				if strings.HasPrefix(modal, "╭") {
					inside = true
				}
				if inside {
					body = append(body, strings.TrimSpace(strings.Trim(modal, "│ ")))
				}
				if inside && strings.HasPrefix(modal, "╰") {
					break
				}
			}
			found := false
			for _, line := range body {
				_, value, ok := strings.Cut(line, "┆")
				if !ok {
					continue
				}
				value = strings.TrimSpace(value)
				if value == output {
					found = true
				} else if value == "DASH_ONE" || value == "DASH_TWO" {
					t.Fatalf("%s click opened another segment's output:\n%s", output, strings.Join(body, "\n"))
				}
			}
			if !found {
				t.Fatalf("%s click lost retained output:\n%s", output, screen.String())
			}
			if _, err := io.WriteString(outer, "\x1b"); err != nil {
				t.Fatal(err)
			}
			awaitFrame(func(frame string) bool { return !strings.Contains(frame, "y copy") })
		}
		if _, err := io.WriteString(outer, "\x021"); err != nil {
			t.Fatal(err)
		}
	}
	runAppServerPreviewWith(t, provider, proxy, appServerPreview{integration: &integration,
		environment: []string{"PATH=" + execTrackPath(), shellinstrumentation.ShTrackerEnvironment + "=" + tracker},
		codexArgs:   []string{"-c", hook, "-c", state},
		duringTurn:  during, afterPrompt: after,
	})
	reportState := tracked.State()
	if len(reportState.Matches) != 1 || reportState.Matches[0].Report == nil {
		t.Fatalf("missing completed dash observation: %+v", reportState)
	}
	report := reportState.Matches[0].Report
	if !report.Done || report.Code != 1 || len(report.Segments) != 5 {
		t.Fatalf("dash report = %+v", report)
	}
	for i, segment := range report.Segments[:4] {
		elapsed := time.Duration(segment.Timing.ElapsedNS)
		if !segment.Began || !segment.Ended || elapsed <= 0 || (i == 1 && elapsed < time.Second) {
			t.Fatalf("segment %d lacks its own measured timing: %+v", i, segment)
		}
	}
	if report.Segments[4].Began || report.Segments[3].Code != 1 {
		t.Fatalf("dash short-circuit status = %+v", report.Segments)
	}
}

// Real Codex runs a command list through the tracking hook: its command item
// must match the helper's report by thread and script, so each segment gets
// its own row and status.
func TestAppServerExecTrackNativeCodex(t *testing.T) {
	shell := newExecTrackShell(t)
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	// The command hook instruments without executing the command script.
	hook, state, _, err := shellhook.Config(helper, "", "", "", shellhook.Invocation{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	integration := testAppServerIntegration(proxy)
	integration.execTrack = shell.hub.Observer
	// Match the launcher: the parent test command's identity is not the new
	// Codex process's hook environment. Codex injects identity into its tools.
	environment := []string{"CODEX_THREAD_ID="}
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
	runAppServerPreviewWith(t, provider, proxy, appServerPreview{integration: &integration,
		environment: environment, afterPrompt: after,
		codexArgs: []string{"-c", hook, "-c", state},
	})
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
			integration := testAppServerIntegration(proxy)
			integration.execTrack = shell.hub.Observer
			var environment []string
			for _, entry := range shell.env {
				if strings.HasPrefix(entry, "BASH_ENV=") {
					environment = append(environment, entry)
				}
			}
			command := "{ echo > /dev/tcp/127.0.0.1/9; } 2>&1 | grep -o 'Operation not permitted'; echo SEG_ONE; echo SEG_TWO >&2; false && echo SEG_NEVER"
			provider := &toolFrontendCodexProvider{
				program:   `const result = await tools.exec_command({cmd:` + string(responses.MustMarshalJSON(command)) + `}); text(result.output);`,
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
			runAppServerPreviewWith(t, provider, proxy, appServerPreview{integration: &integration,
				environment: environment,
				codexArgs:   []string{"-c", `sandbox_mode="` + sandbox + `"`, "-c", `approval_policy="on-request"`},
				approvals:   true,
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
