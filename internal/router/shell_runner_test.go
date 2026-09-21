package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type shellWorkerTestInvocation struct {
	directory   string
	environment []string
}

func newShellWorkerTestInvocation(directory string, environment ...string) shellWorkerTestInvocation {
	base := append(os.Environ(), "CODEX_THREAD_ID=")
	return shellWorkerTestInvocation{
		directory:   directory,
		environment: append(base, environment...),
	}
}

func runShellWorkerTest(
	t *testing.T,
	registry *toolRegistry,
	interpreter string,
	interpreterArguments []string,
	script string,
	stdin *os.File,
	invocations ...shellWorkerTestInvocation,
) (stdout, stderr string, exitCode int) {
	t.Helper()
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	shell, ok := registry.contribution("shell")
	if !ok {
		t.Fatal("shell contribution is unavailable")
	}
	arguments := []string{interpreter}
	arguments = append(arguments, interpreterArguments...)
	if len(invocations) > 1 {
		t.Fatal("runShellWorkerTest accepts at most one invocation")
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "CODEX_THREAD_ID=")
	if len(invocations) == 1 {
		workingDirectory = invocations[0].directory
		environment = invocations[0].environment
	}
	environment = prependToolFrontendPath(environment, registry.frontendDirectory)
	environment = append(environment, routerTestWorkerEnvironment+"=1")
	workerScope := ""
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, routerTestWorkerUnscopedEnvironment+"="); ok {
			workerScope = value
		}
	}
	if workerScope == "" {
		environment = append(environment, routerTestWorkerUnscopedEnvironment+"=1")
	}
	ctx := t.Context()
	release := func() {}
	if workerScope == "0" {
		thread := ""
		for _, entry := range environment {
			if value, ok := strings.CutPrefix(entry, "CODEX_THREAD_ID="); ok {
				thread = value
			}
		}
		ctx, release, err = store.beginSession(ctx, thread, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	defer release()
	arguments = append(arguments, script)
	execution, err := executeShellTool(
		ctx,
		manifest,
		registry.RuntimeRoot,
		&shell,
		arguments,
		stdin,
		workingDirectory,
		environment,
		discoverShellCommentary(registry.shellRuntime),
		nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return execution.Stdout, execution.Stderr, execution.ExitCode
}

func prependToolFrontendPath(environment []string, frontendDirectory string) []string {
	result := make([]string, 0, len(environment)+1)
	path := ""
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
			continue
		}
		result = append(result, entry)
	}
	if path == "" {
		path = frontendDirectory
	} else {
		path = frontendDirectory + string(os.PathListSeparator) + path
	}
	return append(result, "PATH="+path)
}

func TestShellRunnerJoinsIndependentBackgroundJobsAndPreservesFailure(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, interpreter := range []string{"bash", "sh"} {
		t.Run(interpreter, func(t *testing.T) {
			invocation := newShellWorkerTestInvocation(t.TempDir())
			// Each job needs the other's marker, so sequential execution fails
			// instead of passing a timing-only assertion. Polling is bounded.
			script := `
check_one() {
	: > first.started
	attempt=0
	until test -f second.started; do
		attempt=$((attempt + 1))
		test "$attempt" -lt 250 || return 90
		sleep 0.02
	done
	printf 'first finished\n'
	return 7
}
check_two() {
	: > second.started
	attempt=0
	until test -f first.started; do
		attempt=$((attempt + 1))
		test "$attempt" -lt 250 || return 91
		sleep 0.02
	done
	printf 'second finished\n'
}
check_one &
first_check_pid=$!
check_two &
second_check_pid=$!
checks_status=0
wait "$first_check_pid" || checks_status=$?
wait "$second_check_pid" || checks_status=$?
printf 'joined both\n'
exit "$checks_status"
`
			stdout, stderr, exitCode := runShellWorkerTest(t, registry, interpreter, nil, script, nil, invocation)
			if exitCode != 7 || withoutShellChangeNotices(stderr) != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q", exitCode, stdout, stderr)
			}
			if !strings.HasSuffix(stdout, "joined both\n") ||
				strings.Count(stdout, "first finished\n") != 1 ||
				strings.Count(stdout, "second finished\n") != 1 {
				t.Fatalf("jobs were not both joined: %q", stdout)
			}
		})
	}
}

func TestShellRunnerUsesInterpreterBasenameForLanguageVariant(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)

	for _, interpreter := range []string{"bash", "/usr/bin/bash"} {
		t.Run(interpreter, func(t *testing.T) {
			stdout, stderr, exitCode := runShellWorkerTest(
				t,
				registry,
				interpreter,
				nil,
				"values=(zero one); printf '%s' \"${values[1]}\"",
				nil,
			)
			if exitCode != 0 || stdout != "one" || stderr != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q", exitCode, stdout, stderr)
			}
		})
	}

	for _, interpreter := range []string{"sh", "/bin/sh"} {
		t.Run(interpreter+" POSIX", func(t *testing.T) {
			stdout, stderr, exitCode := runShellWorkerTest(
				t,
				registry,
				interpreter,
				nil,
				"value=posix; printf '%s' \"$value\"",
				nil,
			)
			if exitCode != 0 || stdout != "posix" || stderr != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q", exitCode, stdout, stderr)
			}
		})
		t.Run(interpreter+" rejects Bash", func(t *testing.T) {
			_, stderr, exitCode := runShellWorkerTest(
				t,
				registry,
				interpreter,
				nil,
				"values=(zero one)",
				nil,
			)
			if exitCode == 0 || !strings.Contains(stderr, "feature") {
				t.Fatalf("exit %d, stderr %q", exitCode, stderr)
			}
		})
	}

	stdout, stderr, exitCode := runShellWorkerTest(
		t,
		registry,
		"/usr/bin/bash",
		[]string{"-u", "--", "zero", "one"},
		"printf '%s' \"$2\"",
		nil,
	)
	if exitCode != 0 || stdout != "one" || stderr != "" {
		t.Fatalf("argument handling: exit %d, stdout %q, stderr %q", exitCode, stdout, stderr)
	}
}

func TestShellRunnerEvaluatesToolFrontendsWithSnapshotWrappers(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, name := range []string{"mcat", "hgrep", "msymbol", "inspect_file"} {
		if _, ok := registry.wrapper(name); !ok {
			t.Fatalf("tool %q has no authenticated snapshot wrapper", name)
		}
	}

	workspace := t.TempDir()
	nested := filepath.Join(workspace, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "space name.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(workspace, "PATH=/usr/bin:/bin")

	for _, interpreter := range []string{"bash", "/bin/sh"} {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, exitCode := runShellWorkerTest(
				t,
				registry,
				interpreter,
				nil,
				"cd nested\nmcat 'space name.txt' 1:1 | { read -r row; printf 'row:%s\\n' \"$row\"; }\nmcat missing 2>/dev/null || printf recovered",
				nil,
				invocation,
			)
			if exitCode != 0 || stdout != "row:alpha\nrecovered" || stderr != "" {
				t.Fatalf("%s: exit %d, stdout %q, stderr %q", interpreter, exitCode, stdout, stderr)
			}
		})
	}
}

func TestShellRunnerQueriesCurrentSymbol(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	workspace := t.TempDir()
	caller := t.TempDir()
	bin := t.TempDir()
	source := filepath.Join(workspace, "source.go")
	if err := os.WriteFile(source, []byte("package p\nfunc Pick() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", source+":2:6-10")
	if err := os.WriteFile(filepath.Join(bin, "gopls"), []byte(resolver), 0o700); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(caller, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	alias := filepath.Join(caller, "project")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "/bin/sh", nil,
		fmt.Sprintf("msymbol --workspace %q refs source.go 2 Pick", alias), nil, invocation)
	if status != 0 || !strings.Contains(stdout, fmt.Sprintf("%q:2 ", source)) ||
		!strings.Contains(stdout, "func Pick() {}") || !strings.Contains(stderr, "(current snapshot)") {
		t.Fatalf("semantic lookup: stdout=%q stderr=%q exit=%d", stdout, stderr, status)
	}
}

func TestShellRunnerInspectsOutsideFile(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "value.json")
	if err := os.WriteFile(outside, []byte(`{"answer":{"nested":42},"other":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(workspace)
	stdout, stderr, status := runShellWorkerTest(t, registry, "/bin/sh", nil,
		fmt.Sprintf("inspect_file %q", outside), nil, invocation)
	var result struct {
		OK   bool `json:"ok"`
		Data struct {
			Outline []struct {
				Pointer string `json:"pointer"`
			} `json:"outline"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if status != 0 || stderr != "" || !result.OK || len(result.Data.Outline) != 4 ||
		result.Data.Outline[1].Pointer != "/answer" {
		t.Fatalf("inspection: stdout=%s stderr=%q exit=%d", stdout, stderr, status)
	}
}

func TestShellRunnerReadsFormerShellPathsAsWorkspaceFiles(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t)
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "@shell"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "@shell", "script"), []byte("workspace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		"mcat @shell/script",
		"mcat @shell/script @shell/script",
	} {
		t.Run(source, func(t *testing.T) {
			stdout, stderr, code := runShellWorkerTest(t, proxy.registry, "bash", nil, source, nil, newShellWorkerTestInvocation(directory))
			if code != 0 || !strings.Contains(stdout, "workspace") {
				t.Fatalf("workspace read = %q, %q, exit %d", stdout, stderr, code)
			}
		})
	}
}

func TestShellRunnerPreservesStdinAndExternalCommands(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	inputPath := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(inputPath, []byte("stream\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	stdout, stderr, exitCode := runShellWorkerTest(
		t,
		registry,
		"/usr/bin/bash",
		nil,
		"read -r value\nprintf 'stdin:%s\\n' \"$value\"\nprintf external | tr a-z A-Z",
		input,
	)
	if exitCode != 0 || stdout != "stdin:stream\nEXTERNAL" || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", exitCode, stdout, stderr)
	}
}

func TestShellRunnerBoundsAndValidatesOutput(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	shell, ok := registry.contribution("shell")
	if !ok {
		t.Fatal("shell contribution is unavailable")
	}
	runtimeRoot := filepath.Join(registry.SnapshotDir, manifest.RuntimeRoot)

	invocation := newShellWorkerTestInvocation(t.TempDir())

	started := time.Now()
	captureBudget := 16<<20 - len(shellOverflowDiagnostic) - 3
	execution, err := executeShellProgram(
		t.Context(),
		manifest,
		runtimeRoot,
		&shell,
		[]string{"bash", fmt.Sprintf(
			`python3 -c 'import subprocess, sys; subprocess.Popen(["sleep", "10"]); sys.stdout.buffer.write(b"a"*%d + "😀".encode()); sys.stdout.buffer.flush()'`,
			captureBudget-1,
		)},
		nil,
		invocation.directory,
		invocation.environment,
		nil,
		nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if execution.ExitCode != 1 || !strings.Contains(execution.Stderr, "interpreter output exceeds") ||
		len(execution.Stdout) != captureBudget-1 || !utf8.ValidString(execution.Stdout) ||
		len(execution.Stdout)+len(execution.Stderr) > 16<<20 || time.Since(started) >= 5*time.Second {
		t.Fatalf(
			"overflow: exit %d, stdout bytes %d, stderr %q",
			execution.ExitCode,
			len(execution.Stdout),
			execution.Stderr,
		)
	}

	execution, err = executeShellProgram(
		t.Context(),
		manifest,
		runtimeRoot,
		&shell,
		[]string{"bash", `python3 -c 'import os; os.write(1, b"\xff")'`},
		nil,
		invocation.directory,
		invocation.environment,
		nil,
		nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if execution.ExitCode != 1 || execution.Stdout != "" ||
		execution.Stderr != "shell: interpreter output is not UTF-8\n" {
		t.Fatalf(
			"UTF-8: exit %d, stdout %q, stderr %q",
			execution.ExitCode,
			execution.Stdout,
			execution.Stderr,
		)
	}
}

func TestShellOutputStreamingBoundaries(t *testing.T) {
	var output bytes.Buffer
	canceled := false
	capture := newShellOutputCapture(func() { canceled = true })
	capture.stdout.destination = &output
	for _, part := range []string{"first", "\xf0\x9f", "\x98\x80", "\xffbad"} {
		if _, err := capture.stdout.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	stdout, _, overflow, _ := capture.result()
	if output.String() != "first😀" || stdout != "\xffbad" || overflow || canceled {
		t.Fatalf("stream = %q, remaining = %q, overflow %t, canceled %t", output.String(), stdout, overflow, canceled)
	}

	// Background writes after the terminal snapshot must not escape into a
	// completed host result, even when they arrive concurrently.
	before := output.String()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = capture.stdout.Write([]byte("late"))
	}()
	<-done
	if output.String() != before {
		t.Fatal("output forwarded after terminal snapshot")
	}
	capture = newShellOutputCapture(func() { canceled = true })
	capture.stdout.destination = &output
	capture.remaining = 3
	_, _ = capture.stdout.Write([]byte("abcd"))
	stdout, _, overflow, _ = capture.result()
	if stdout != "" || !overflow || !canceled || !strings.HasSuffix(output.String(), "abc") {
		t.Fatal("streaming bypassed the output budget")
	}

	canceled = false
	capture = newShellOutputCapture(func() { canceled = true })
	capture.stdout.destination = shellFailingOutputWriter{}
	_, err := capture.stdout.Write([]byte("read"))
	if !errors.Is(err, io.ErrClosedPipe) || !canceled || !errors.Is(capture.writeErr, io.ErrClosedPipe) {
		t.Fatalf("write failure = %v, canceled %t", err, canceled)
	}
}

type shellFailingOutputWriter struct{}

func (shellFailingOutputWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestShellOutputFinalizationWaitsForActiveWrite(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	capture := newShellOutputCapture(func() {})
	capture.stderr.destination = shellBlockedOutputWriter{entered, release}
	written := make(chan error, 1)
	go func() {
		_, err := capture.stderr.Write([]byte("failure"))
		written <- err
	}()
	<-entered
	finalized := make(chan error, 1)
	go func() {
		_, _, _, err := capture.result()
		finalized <- err
	}()
	close(release)
	if err := <-written; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error = %v", err)
	}
	if err := <-finalized; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("terminal error = %v", err)
	}
	// A post-close write must not call the destination again (it would panic
	// closing entered twice), or change the captured terminal error.
	if n, err := capture.stderr.Write([]byte("late")); n != 4 || err != nil {
		t.Fatalf("post-close write = %d, %v", n, err)
	}
}

type shellBlockedOutputWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (writer shellBlockedOutputWriter) Write([]byte) (int, error) {
	close(writer.entered)
	<-writer.release
	return 0, io.ErrClosedPipe
}
