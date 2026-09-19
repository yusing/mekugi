//go:build unix

package toolplugin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestShellOverflowRetiresInheritedPipeDescendants(t *testing.T) {
	t.Parallel()
	snapshot, err := Load(t.Context(), t.TempDir(), filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []struct {
		stream     string
		input      bool
		descriptor bool
	}{{"stdout", true, false}, {"stderr", true, false}, {"stdout", false, false}, {"stdout", true, true}} {
		t.Run(mode.stream+"/input="+strconv.FormatBool(mode.input)+"/descriptor="+strconv.FormatBool(mode.descriptor), func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			timerDurationPath := filepath.Join(directory, "drain-duration")
			timerPreload := filepath.Join(directory, "timers.cjs")
			if err := os.WriteFile(timerPreload, []byte(controlledHostTimersPreload), 0600); err != nil {
				t.Fatal(err)
			}
			pidPath := filepath.Join(directory, "descendant.pid")
			t.Cleanup(func() {
				encoded, _ := os.ReadFile(pidPath)
				if pid, err := strconv.Atoi(string(encoded)); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			script := `const {spawn} = require("node:child_process");
const {writeFileSync} = require("node:fs");
const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {stdio: "inherit"});
writeFileSync(` + strconv.Quote(pidPath) + `, String(child.pid));
process.` + mode.stream + `.write("x".repeat(4096));
setInterval(() => {}, 1000);
`
			interpreter := snapshot.NodeExecutable
			if mode.descriptor {
				interpreter, err = exec.LookPath("perl")
				if err != nil {
					t.Skip("Perl is required for the streaming-descriptor regression")
				}
				// BEGIN executes during parsing, before the large trailing comment
				// can drain from the host's script pipe. The fork retains both pipes.
				script = `BEGIN {
my $pid = fork();
die "fork failed" unless defined $pid;
if ($pid == 0) { sleep 30; exit 0; }
open(my $out, ">", ` + strconv.Quote(pidPath) + `) or die $!;
print $out $pid;
close($out);
$| = 1;
print "x" x (256 * 1024);
}
#` + strings.Repeat("padding", 16000) + "\n"
			}
			// A deadline also reaps this fixture's group if the regression recurs.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			input, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			scriptRead, scriptWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer scriptRead.Close()
			defer scriptWrite.Close()
			request := map[string]any{
				"operation": "execute", "snapshotRoot": filepath.Join(snapshot.Root, snapshotDirectory),
				"module": "builtin/tools.js", "index": 4,
				"arguments": []string{interpreter, script},
				"inputFD":   mode.input, "outputBudgetBytes": 128,
			}
			var response executionResponse
			started := time.Now()
			var inheritedInput *os.File
			var scriptFiles []*os.File
			if mode.input {
				inheritedInput = input
				scriptFiles = []*os.File{scriptRead, scriptWrite}
			}
			nodeOptions := strings.TrimSpace(os.Getenv("NODE_OPTIONS") + " --require=" + strconv.Quote(timerPreload))
			environment := append(os.Environ(), "NODE_OPTIONS="+nodeOptions,
				"FIXTURE_HOST="+filepath.Join(snapshot.Root, hostFilename), "FIXTURE_CLEANUP_DURATION="+timerDurationPath,
				"FIXTURE_ACCELERATE_CLEANUP=1")
			err = invoke(ctx, snapshot.NodeExecutable, filepath.Join(snapshot.Root, hostFilename), "", directory,
				environment, 4096, inheritedInput, scriptFiles, request, &response)
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(started) >= 3*time.Second {
				t.Fatal("overflow cleanup did not finish promptly")
			}
			if response.ExitCode != 1 || !strings.Contains(response.Stderr, "interpreter output exceeds 128 bytes") {
				t.Fatalf("overflow result = %+v", response)
			}
			if len(response.Stdout)+len(response.Stderr) > 128 {
				t.Fatal("overflow result exceeds budget")
			}
			if encoded, err := os.ReadFile(timerDurationPath); err != nil || string(encoded) != "1000" {
				t.Fatalf("controlled shell drain duration = %q, %v; want %q", encoded, err, "1000")
			}
			encoded, err := os.ReadFile(pidPath)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(encoded))
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("descendant %d survived overflow", pid)
		})
	}
}

func TestShellSuccessfulBackgroundProcessIsNotRetired(t *testing.T) {
	t.Parallel()
	snapshot, err := Load(t.Context(), t.TempDir(), filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "background.pid")
	t.Cleanup(func() {
		encoded, _ := os.ReadFile(pidPath)
		if pid, err := strconv.Atoi(string(encoded)); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	script := `const {spawn} = require("node:child_process");
const {writeFileSync} = require("node:fs");
const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {stdio: "ignore"});
writeFileSync(` + strconv.Quote(pidPath) + `, String(child.pid));
child.unref();
process.stdout.write("ok");`
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := Execute(ctx, snapshot.NodeExecutable, snapshot.Root, "builtin/tools.js", 4,
		[]string{snapshot.NodeExecutable, script}, input, directory, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "ok" || result.Stderr != "" {
		t.Fatalf("success result = %+v", result)
	}
	encoded, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("successful background process was retired: %v", err)
	}
}

func TestShellDescriptorDeliveryPreservesProgramInput(t *testing.T) {
	t.Parallel()
	perl, err := exec.LookPath("perl")
	if err != nil {
		t.Skip("Perl is required for the streaming-descriptor regression")
	}
	snapshot, err := Load(t.Context(), t.TempDir(), filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	inputPath := filepath.Join(directory, "input")
	if err := os.WriteFile(inputPath, []byte("program data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	script := "#" + strings.Repeat("padding", 16000) + "\nprint scalar <STDIN>;\n"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := Execute(ctx, snapshot.NodeExecutable, snapshot.Root, "builtin/tools.js", 4,
		[]string{perl, script}, input, directory, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "program data\n" || result.Stderr != "" {
		t.Fatalf("descriptor execution = %+v", result)
	}
}
