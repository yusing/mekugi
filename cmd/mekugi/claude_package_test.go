//go:build unix

package main

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// TestClaudePackagedLaunch exercises the real distribution without inference or
// authentication. Set MEKUGI_TEST_CLAUDE_PACKAGE to an assembled directory with
// mekugi, mekugi-exec, and claude-bridge. The SDK and Node are real; only the
// installed official CLI is replaced by a stream-json initialization fixture.
func TestClaudePackagedLaunch(t *testing.T) {
	packageDirectory := os.Getenv("MEKUGI_TEST_CLAUDE_PACKAGE")
	if packageDirectory == "" {
		t.Skip("set MEKUGI_TEST_CLAUDE_PACKAGE to an assembled offline distribution")
	}
	packageDirectory, err := filepath.Abs(packageDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mekugi", "mekugi-exec", "claude-bridge/dist/bridge.js", "claude-bridge/package.json", "claude-bridge/node_modules/@anthropic-ai/claude-agent-sdk/package.json"} {
		if _, err := os.Stat(filepath.Join(packageDirectory, name)); err != nil {
			t.Fatalf("distribution missing %s: %v", name, err)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("opt-in package test requires installed Node.js")
	}
	node, err = filepath.Abs(node)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("tar"); err != nil {
		t.Fatal("opt-in package test requires tar")
	}
	directory := t.TempDir()
	archive := filepath.Join(directory, "distribution.tar.gz")
	relocated := filepath.Join(directory, "relocated package with spaces")
	if err := os.Mkdir(relocated, 0700); err != nil {
		t.Fatal(err)
	}
	// Round-trip only the distribution contents, not the repository or build
	// directory. Neither launch receives a bridge override or repository cwd.
	for _, args := range [][]string{{"-czf", archive, "-C", packageDirectory, "."}, {"-xzf", archive, "-C", relocated}} {
		command := exec.CommandContext(t.Context(), "tar", args...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("archive round-trip failed: %v\n%s", err, output)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(relocated, "claude-bridge", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Private bool `json:"private"`
	}
	if err := json.Unmarshal(manifest, &metadata); err != nil || !metadata.Private {
		t.Fatal("packaged bridge must retain its private package manifest")
	}
	nativePackages, err := filepath.Glob(filepath.Join(relocated, "claude-bridge", "node_modules", "@anthropic-ai", "claude-agent-sdk-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(nativePackages) != 0 {
		t.Fatal("package must omit optional native SDK runtime packages; it uses the installed official CLI")
	}
	if _, err := os.Stat(filepath.Join(relocated, "claude-bridge", "node_modules", "typescript")); !os.IsNotExist(err) {
		t.Fatal("package must omit the development TypeScript compiler")
	}
	for _, launch := range []string{"direct", "symlink", "symlink-temp"} {
		t.Run(launch, func(t *testing.T) {
			fixture := t.TempDir()
			if launch == "symlink-temp" {
				alias := filepath.Join(t.TempDir(), "temporary-root")
				if err := os.Symlink(fixture, alias); err != nil {
					t.Fatal(err)
				}
				fixture = alias
			}
			// macOS temporary roots can traverse /var -> /private/var.
			// Private state needs a canonical path, as does the cwd receipt.
			fixture, err := filepath.EvalSymlinks(fixture)
			if err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(fixture, "unrelated working directory")
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			fakeCLI := filepath.Join(fixture, "claude")
			if err := os.WriteFile(fakeCLI, []byte(packagedClaudeFixture), 0700); err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(relocated, "mekugi")
			if launch == "symlink" {
				link := filepath.Join(fixture, "mekugi")
				if err := os.Symlink(executable, link); err != nil {
					t.Fatal(err)
				}
				executable = link
			}
			reportPath := filepath.Join(fixture, "cli-report.json")
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "claude")
			command.Dir = workspace
			// Do not inherit authentication, Node injection, bridge overrides, or
			// the user's configuration. The fake CLI cannot contact a provider.
			command.Env = []string{
				"PATH=" + fixture + string(os.PathListSeparator) + filepath.Dir(node) + string(os.PathListSeparator) + os.Getenv("PATH"),
				"HOME=" + fixture,
				"XDG_CONFIG_HOME=" + filepath.Join(fixture, "config"),
				"XDG_STATE_HOME=" + filepath.Join(fixture, "state"),
				"MEKUGI_RUNTIME_DIR=" + filepath.Join(fixture, "runtime"),
				"CLAUDE_CONFIG_DIR=" + filepath.Join(fixture, "claude-config"),
				"CLAUDE_CODE_SHELL=/bin/bash",
				"MEKUGI_TEST_PACKAGE_CLI_REPORT=" + reportPath,
				"TERM=xterm-256color",
			}
			master, err := pty.StartWithSize(command, &pty.Winsize{Cols: 120, Rows: 30})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			processDone := make(chan struct{})
			go func() { defer close(processDone); done <- command.Wait() }()
			chunks := make(chan []byte, 64)
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				buffer := make([]byte, 8192)
				for {
					n, err := master.Read(buffer)
					if n > 0 {
						select {
						case chunks <- bytes.Clone(buffer[:n]):
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			completed := false
			t.Cleanup(func() {
				if !completed {
					// pty.Start creates a new session. On a failed assertion, also
					// stop this fixture's bridge and SDK CLI, not just its UI.
					_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				}
				cancel()
				master.Close()
				<-readerDone
				<-processDone
			})
			screen := vt.NewEmulator(120, 30)
			defer screen.Close()
			var pending []byte
			ready := false
			for !ready {
				select {
				case chunk := <-chunks:
					pending = append(pending, chunk...)
					for {
						at := bytes.Index(pending, []byte("\x1b[?2026l"))
						if at < 0 {
							break
						}
						at += len("\x1b[?2026l")
						frame := bytes.ReplaceAll(pending[:at], []byte("\x1b]10;?\x1b\\"), nil)
						frame = bytes.ReplaceAll(frame, []byte("\x1b]11;?\x1b\\"), nil)
						if _, err := screen.Write(frame); err != nil {
							t.Fatal(err)
						}
						pending = pending[at:]
						// This is readiness/lifecycle coverage, not a UI golden.
						ready = ready || strings.Contains(screen.String(), "Ready")
					}
				case err := <-done:
					t.Fatalf("packaged Claude exited before readiness: %v\n%s\n%s", err, screen.String(), pending)
				case <-ctx.Done():
					t.Fatalf("packaged Claude did not become ready\n%s", screen.String())
				}
			}
			if _, err := master.Write([]byte("/quit\r")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("packaged Claude shutdown failed: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("packaged Claude shutdown timed out")
			}
			data, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatal("real SDK did not launch the installed CLI fixture")
			}
			var report struct {
				Args        []string `json:"args"`
				Cwd         string   `json:"cwd"`
				Initialized bool     `json:"initialized"`
				Stopped     bool     `json:"stopped"`
				Prompts     int      `json:"prompts"`
				GuardProbe  bool     `json:"guardProbe"`
			}
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			if !report.Initialized || !report.Stopped || !report.GuardProbe || report.Prompts != 0 || report.Cwd != workspace {
				t.Fatalf("SDK initialization, workspace, or clean shutdown mismatch: %+v", report)
			}
			for _, flag := range []string{"--input-format", "--output-format"} {
				index := slices.Index(report.Args, flag)
				if index < 0 || index+1 >= len(report.Args) || report.Args[index+1] != "stream-json" {
					t.Fatalf("SDK did not use official CLI %s stream-json", flag)
				}
			}
			completed = true
		})
	}
}

const packagedClaudeFixture = `#!/usr/bin/env node
const fs = require('node:fs');
const readline = require('node:readline');
const {spawnSync} = require('node:child_process');
const report = {args: process.argv.slice(2), cwd: process.cwd(), initialized: false, stopped: false, prompts: 0, guardProbe: false};
const settingsArg = report.args[report.args.indexOf('--settings') + 1];
const settings = JSON.parse(settingsArg.startsWith('{') ? settingsArg : fs.readFileSync(settingsArg, 'utf8'));
const hooks = Object.entries(settings.hooks).flatMap(([event, groups]) => groups.flatMap(group => group.hooks.map(hook => ({
  event, matcher: group.matcher || '', source: 'flagSettings', type: hook.type,
  commandText: [hook.command, ...hook.args].join(' ')
}))));
const save = () => fs.writeFileSync(process.env.MEKUGI_TEST_PACKAGE_CLI_REPORT, JSON.stringify(report));
const stop = () => { report.stopped = true; save(); process.exit(0); };
save();
process.on('SIGTERM', stop);
process.on('SIGINT', stop);
const lines = readline.createInterface({input: process.stdin});
lines.on('close', stop);
lines.on('line', line => {
  const frame = JSON.parse(line);
  if (frame.type === 'user') { report.prompts++; save(); process.exit(91); }
  if (frame.type !== 'control_request') return;
  let response;
  switch (frame.request.subtype) {
    case 'initialize': {
      const hook = settings.hooks.SessionStart[0].hooks[0];
      const result = spawnSync(hook.command, hook.args, {input: JSON.stringify({hook_event_name: 'SessionStart'}), encoding: 'utf8'});
      if (result.status !== 0) process.exit(93);
      report.guardProbe = true;
      report.initialized = true;
      response = {commands: [], models: [], agents: [], account: {}};
      break;
    }
    case 'get_settings': response = {effective: settings, sources: [{source: 'flagSettings', settings}]}; break;
    case 'get_hooks_listing': response = {policy: {allDisabled: false, managedOnly: false, pluginOnly: false, policyHookCount: 0}, hooks}; break;
    default: process.exit(92);
  }
  save();
  process.stdout.write(JSON.stringify({type: 'control_response', response: {
    subtype: 'success', request_id: frame.request_id,
    response
  }}) + '\n');
});
`
