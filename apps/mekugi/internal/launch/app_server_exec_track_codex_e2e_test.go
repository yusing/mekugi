//go:build codex_e2e

package launch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	json "encoding/json/v2"

	"github.com/yusing/mekugi/modules/commandsegments/reports"

	"github.com/yusing/mekugi/backends/codex/shellhook"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Dash has no BASH_ENV or DEBUG trap. The managed host must apply the native
// hook and expose the same streaming, timing and per-command output as Bash.
func TestAppServerExecTrackNativeCodexDash(t *testing.T) {
	resolved, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil || filepath.Base(resolved) != "dash" {
		t.Skip("requires dash-backed /bin/sh")
	}
	shell := newExecTrackShell(t)
	helper, err := execTrackTestHelper()
	if err != nil {
		t.Fatal(err)
	}
	tracker := filepath.Join(shell.root, "exec-track.sh")
	if err := os.WriteFile(tracker, []byte(shellinstrumentation.ShTracker(helper, shell.transport.Endpoints().Requests, shell.transport.Endpoints().Directory)), 0o600); err != nil {
		t.Fatal(err)
	}
	hook, state, _, err := shellhook.Config(helper, tracker, "", shellhook.Invocation{})
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
		program:  `const result = await tools.exec_command({cmd:` + string(responses.MustMarshalJSON(command)) + `,shell:"/bin/sh",login:false,yield_time_ms:30000}); text(result.output);`,
		expected: []string{"DASH_ONE", "DASH_TWO"}, finalText: "Recovered after a retry.",
	}
	during := func(t *testing.T, outer io.Writer, await func(string), _ func(func(string) bool), _ *vt.Emulator) {
		t.Helper()
		if _, err := io.WriteString(outer, "\x05\x05"); err != nil {
			t.Fatal(err)
		}
		await("┆ DASH_ONE")

		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	after := func(t *testing.T, outer io.Writer, await func(string), awaitFrame func(func(string) bool), screen *vt.Emulator) {
		t.Helper()
		await("Skipped echo DASH_NEVER")
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
			// Read only the docked sheet's body, between its heading rule and
			// its controls, excluding the Activity above it.
			var body []string
			inside := false
			for _, line := range strings.Split(screen.String(), "\n") {
				if !inside {
					inside = strings.HasPrefix(strings.TrimLeft(line, " "), "── ")
					continue
				}
				if strings.Contains(line, "y copy") {
					break
				}
				body = append(body, strings.TrimSpace(strings.Trim(line, "│▐ ")))
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
	}
	runAppServerPreviewWith(t, provider, proxy, appServerPreview{managedRuntime: true, integration: &integration,
		environment: []string{"SHELL=/bin/sh", "PATH=" + execTrackPath(), shellinstrumentation.ShTrackerEnvironment + "=" + tracker},
		codexArgs:   []string{"-c", hook, "-c", state, "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`},
		duringTurn:  during, afterPrompt: after,
	})
	names, err := filepath.Glob(filepath.Join(proxy.retained.Store.Directory(), "segments-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report *reports.Report
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			Workspace, CallID string
			Report            reports.Report
		}
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(record.Report.Command, "echo DASH_ONE") {
			continue
		}
		inspected, found, err := testReports().Inspect(proxy.retained.Store.Directory(), record.Workspace, record.CallID)
		if err != nil || !found {
			t.Fatalf("completed dash observation: %v, %v", found, err)
		}
		if err := json.Unmarshal(inspected.Data, &record); err != nil {
			t.Fatal(err)
		}
		report = &record.Report
	}
	if report == nil || report.Exit != 1 || len(report.Parts) != 5 {
		t.Fatalf("dash report = %+v", report)
	}
	for i, part := range report.Parts[:4] {
		elapsed := time.Duration(part.Timing.ElapsedNS)
		if part.Skipped || part.Timing.Started.IsZero() || part.Timing.Ended.IsZero() || elapsed <= 0 || (i == 1 && elapsed < time.Second) {
			t.Fatalf("segment %d lacks measured timing: %+v", i, part)
		}
	}
	if !report.Parts[4].Skipped || report.Parts[3].Exit != 1 {
		t.Fatalf("dash short-circuit status = %+v", report.Parts)
	}

}

// The user hook replaces the model command, the tracker observes that list,
// and the guard denies just one segment. Persist only the executed segments'
// own output, with authored sources and skipped outcomes intact.
func TestAppServerExecTrackNativeCodex(t *testing.T) {
	shell := newExecTrackShell(t)
	helper, err := execTrackTestHelper()
	if err != nil {
		t.Fatal(err)
	}
	frontend := filepath.Join(shell.root, "frontends")
	if err := os.Mkdir(frontend, 0700); err != nil {
		t.Fatal(err)
	}
	environment, err := frontendShellEnvironment([]string{"PATH=" + execTrackPath()}, frontend, helper, shell.transport.Endpoints())
	if err != nil {
		t.Fatal(err)
	}
	// A trusted file-layer hook must still run beside the invocation-layer tracker.
	userHome := t.TempDir()
	marker := filepath.Join(shell.root, "user-hook-ran")
	userHook := filepath.Join(shell.root, "user-hook")
	target := filepath.Join(shell.root, "git")
	ranTarget := filepath.Join(shell.root, "target-ran")
	if err := os.WriteFile(target, []byte("#!/bin/bash\n[ \"$1\" = push ] || exit 1\ntouch "+quoteShellWord(ranTarget)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	deniedCommand := quoteShellWord(target) + " push origin main"
	command := "echo SEG_ONE; " + deniedCommand + " || echo RECOVERED; echo SEG_TWO >&2; false && echo SEG_NEVER"
	reply := responses.MustMarshalJSON(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": map[string]string{"command": command}}})
	if err := os.WriteFile(userHook, []byte("#!/bin/bash\nprintf ran >> "+quoteShellWord(marker)+"\nprintf '%s\\n' "+quoteShellWord(string(reply))+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(userHome, "config.toml")
	userConfig, userState, _, err := shellhook.Config(userHook, "", "", shellhook.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	userState = strings.ReplaceAll(userState, "/<session-flags>/config.toml", configPath)
	if err := os.WriteFile(configPath, []byte(userConfig+"\n"+userState+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	environment = append(environment, "CODEX_HOME="+userHome)
	args, _, err := optionalTrackingHookArgs([]string{"-c", "features.shell_guard=true"}, helper, filepath.Join(shell.root, "exec-track.sh"), "")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	integration := testAppServerIntegration(proxy)
	integration.execTrack = shell.hub.Observer
	bash := execTrackShellExecutable(t, "bash")
	environment = append(environment, "SHELL="+bash)
	provider := &toolFrontendCodexProvider{
		program:  `const result = await tools.exec_command({cmd:"echo MODEL_ORIGINAL",shell:` + string(responses.MustMarshalJSON(bash)) + `,yield_time_ms:30000}); text(result.output);`,
		expected: []string{"SEG_ONE", "RECOVERED", "SEG_TWO"}, finalText: "Recovered after a retry.",
	}
	runAppServerPreviewWith(t, provider, proxy, appServerPreview{managedRuntime: true, integration: &integration, environment: environment, codexArgs: args,
		duringTurn: func(t *testing.T, outer io.Writer, await func(string), _ func(func(string) bool), _ *vt.Emulator) {
			await("Allow this remote write?")
			if _, err := io.WriteString(outer, "fixture denial\r"); err != nil {
				t.Fatal(err)
			}
		},
		afterPrompt: func(t *testing.T, _ io.Writer, await func(string), _ func(func(string) bool), screen *vt.Emulator) {
			await("Skipped echo SEG_NEVER")
			for _, row := range []string{"Ran     echo SEG_ONE", "Ran     echo RECOVERED", "Ran     echo SEG_TWO"} {
				if !strings.Contains(screen.String(), row) {
					t.Fatalf("missing live command row %q:\n%s", row, screen.String())
				}
			}
		},
	})
	if _, err := os.Stat(ranTarget); !os.IsNotExist(err) {
		t.Fatalf("denied git was executed: %v", err)
	}
	if ran, err := os.ReadFile(marker); err != nil || string(ran) != "ran" {
		t.Fatalf("config.toml hook did not run once: %q, %v", ran, err)
	}
	names, err := filepath.Glob(filepath.Join(proxy.retained.Store.Directory(), "segments-*.json"))
	if err != nil || len(names) != 1 {
		t.Fatalf("expected one retained segment report, got %v: %v", names, err)
	}
	data, err := os.ReadFile(names[0])
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Workspace, CallID string
		Report            reports.Report
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if source, ok := shellinstrumentation.CommandScript(record.Report.Command); !ok || source != command {
		t.Fatalf("retained command does not identify the user rewrite: %q", source)
	}
	want := []struct {
		source, output string
		exit           int
		skipped        bool
	}{
		{source: "echo SEG_ONE", output: "SEG_ONE"}, {source: deniedCommand, output: "mekugi: remote write denied: user denied this command with a reason: fixture denial", exit: 1},
		{source: "echo RECOVERED", output: "RECOVERED"}, {source: "echo SEG_TWO >&2", output: "SEG_TWO"},
		{source: "false", exit: 1}, {source: "echo SEG_NEVER", skipped: true},
	}
	completed := reports.Completed{ID: record.CallID, Command: record.Report.Command, Output: record.Report.Output, Exit: 1}
	for _, w := range want {
		completed.Sources = append(completed.Sources, w.source)
	}
	restored, err := testReports().Restore(t.Context(), reports.Scope{Store: proxy.retained.Store, Workspace: record.Workspace}, []reports.Completed{completed})
	if err != nil || restored[record.CallID] == nil {
		t.Fatalf("authored sources could not restore the completed command: %v", err)
	}
	parts := restored[record.CallID].Parts
	if len(parts) != len(want) {
		t.Fatalf("unexpected segment count: %s", data)
	}
	for i, w := range want {
		part := parts[i]
		if part.Source != w.source || part.Exit != w.exit || part.Skipped != w.skipped || part.Skipped && part.Output != nil || !part.Skipped && (part.Output == nil || *part.Output != w.output) {
			t.Fatalf("guarded segment %d lost attribution: %s", i, data)
		}
	}

}

func TestAppServerExecTrackNativeCodexMovedWorkspace(t *testing.T) {
	shell := newExecTrackShell(t)
	helper, err := execTrackTestHelper()
	if err != nil {
		t.Fatal(err)
	}
	bash := execTrackShellExecutable(t, "bash")
	marker, wrapper := filepath.Join(shell.root, "hook-runs"), filepath.Join(shell.root, "hook-helper")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf 'hook\\n' >> "+quoteShellWord(marker)+"\nexec "+quoteShellWord(helper)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hook, state, _, err := shellhook.Config(wrapper, "", "", shellhook.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	integration := testAppServerIntegration(proxy)
	integration.execTrack = shell.hub.Observer
	environment := []string{"SHELL=" + bash, "PATH=" + execTrackPath()}
	for _, entry := range shell.env {
		if strings.HasPrefix(entry, "BASH_ENV=") {
			environment = append(environment, entry)
		}
	}
	moved := filepath.Join(t.TempDir(), "moved-workspace")
	provider := &toolFrontendCodexProvider{
		program: `text((await tools.exec_command({cmd:` + string(responses.MustMarshalJSON("mv \"$PWD\" "+quoteShellWord(moved)+"; printf MOVED")) + `,shell:` + string(responses.MustMarshalJSON(bash)) + `,login:false})).output);
	text((await tools.exec_command({cmd:"git --version; printf AFTER; false && printf NEVER",shell:` + string(responses.MustMarshalJSON(bash)) + `,login:false,workdir:` + string(responses.MustMarshalJSON(moved)) + `})).output);`,
		expected: []string{"MOVED", "AFTER"}, finalText: "Recovered after a retry.",
	}
	after := func(t *testing.T, outer io.Writer, _ func(string), awaitFrame func(func(string) bool), _ *vt.Emulator) {
		t.Helper()
		if _, err := io.WriteString(outer, "\x05\x05"); err != nil {
			t.Fatal(err)
		}
		awaitFrame(func(frame string) bool {
			return strings.Contains(frame, "Skipped printf NEVER") && strings.Contains(frame, "┆ AFTER")
		})
		runs, err := os.ReadFile(marker)
		if err != nil || string(runs) != "hook\n" {
			t.Fatalf("expected one hook before relocation and none afterward: %q, %v", runs, err)
		}
	}
	runAppServerPreviewWith(t, provider, proxy, appServerPreview{managedRuntime: true, integration: &integration,
		environment: environment, afterPrompt: after, codexArgs: []string{"-c", hook, "-c", state},
	})
}

// Shell segments must still report their output and status when the sandbox
// denies a command network access.
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
			bash := execTrackShellExecutable(t, "bash")
			environment := []string{"SHELL=" + bash}
			for _, entry := range shell.env {
				if strings.HasPrefix(entry, "BASH_ENV=") {
					environment = append(environment, entry)
				}
			}
			command := "{ echo > /dev/tcp/127.0.0.1/9; } 2>&1 | grep -o 'Operation not permitted'; echo SEG_ONE; echo SEG_TWO >&2; false && echo SEG_NEVER"
			provider := &toolFrontendCodexProvider{
				program:   `const result = await tools.exec_command({cmd:` + string(responses.MustMarshalJSON(command)) + `,shell:` + string(responses.MustMarshalJSON(bash)) + `,login:false}); text(result.output);`,
				expected:  []string{"Operation not permitted", "SEG_ONE", "SEG_TWO"},
				finalText: "Recovered after a retry.",
			}
			after := func(t *testing.T, outer io.Writer, _ func(string), awaitFrame func(func(string) bool), _ *vt.Emulator) {
				t.Helper()
				if _, err := io.WriteString(outer, "\x05\x05"); err != nil {
					t.Fatal(err)
				}
				awaitFrame(func(frame string) bool {
					for _, want := range []string{"echo SEG_ONE", "echo SEG_TWO", "false · exit 1", "┆ SEG_ONE", "┆ SEG_TWO", "Skipped echo SEG_NEVER"} {
						if !strings.Contains(frame, want) {
							return false
						}
					}
					return true
				})
			}
			runAppServerPreviewWith(t, provider, proxy, appServerPreview{managedRuntime: true, integration: &integration,
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
