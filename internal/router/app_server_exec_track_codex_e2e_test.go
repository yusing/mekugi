//go:build journal_e2e

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

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/vcsguard"
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
	if err := os.WriteFile(tracker, []byte(execsegment.ShTracker(helper, shell.hub.requests.Name(), shell.hub.directory)), 0o600); err != nil {
		t.Fatal(err)
	}
	hook, state, err := vcsguard.HookConfig(helper, "")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	proxy.execTrack = shell.hub
	gate := filepath.Join(shell.root, "continue")
	command := "echo DASH_ONE; { sleep 1; while [ ! -f " + quoteShellWord(gate) + " ]; do sleep 0.05; done; }; echo DASH_TWO >&2; false && echo DASH_NEVER"
	provider := &toolFrontendCodexProvider{
		program:  `const result = await tools.exec_command({cmd:` + string(mustMarshalJSON(command)) + `,shell:"/bin/sh",login:false}); text(result.output);`,
		expected: []string{"DASH_ONE", "DASH_TWO"}, finalText: "Recovered after a retry.",
	}
	var report *execTrack
	during := func(t *testing.T, outer io.Writer, await func(string), _ func(func(string) bool), _ *vt.Emulator) {
		t.Helper()
		if _, err := io.WriteString(outer, "\x023"); err != nil {
			t.Fatal(err)
		}
		await("┆ DASH_ONE")
		shell.hub.mu.Lock()
		for _, track := range shell.hub.tracks {
			if track.script == command && !track.ended {
				report = track
			}
		}
		shell.hub.mu.Unlock()
		if report == nil {
			t.Fatal("streamed output has no live dash report")
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
	runAppServerPreviewWith(t, provider, proxy, appServerPreview{
		environment: []string{"PATH=" + execTrackPath(), execsegment.ShTrackerEnvironment + "=" + tracker},
		codexArgs:   []string{"-c", hook, "-c", "hooks.state={" + state + "}"},
		noJournal:   true, duringTurn: during, afterPrompt: after,
	})
	if !report.done || report.code != 1 || len(report.segments) != 5 {
		t.Fatalf("dash report = %+v", report)
	}
	for i, segment := range report.segments[:4] {
		elapsed := time.Duration(segment.timing.ElapsedNS)
		if !segment.began || !segment.ended || elapsed <= 0 || (i == 1 && elapsed < time.Second) {
			t.Fatalf("segment %d lacks its own measured timing: %+v", i, segment)
		}
	}
	if report.segments[4].began || report.segments[3].code != 1 {
		t.Fatalf("dash short-circuit status = %+v", report.segments)
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
	// A native PreToolUse handler inherits BASH_ENV but must not enter the
	// tracker, even while the same startup file tracks the actual tool command.
	hookHelper := filepath.Join(shell.root, "hook-helper")
	source := "#!/bin/sh\nif [ -n \"${MEKUGI_EXEC_TRACK-}\" ]; then echo 'hook started tracking' >&2; exit 2; fi\nexec " + quoteShellWord(helper) + " \"$@\"\n"
	if err := os.WriteFile(hookHelper, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	hook, state, err := vcsguard.HookConfig(hookHelper, "")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	proxy.execTrack = shell.hub
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
	runAppServerPreviewWith(t, provider, proxy, appServerPreview{
		environment: environment, afterPrompt: after, noJournal: true,
		codexArgs: []string{"-c", hook, "-c", "hooks.state={" + state + "}"},
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
