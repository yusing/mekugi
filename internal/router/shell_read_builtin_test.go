package router

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func TestParseShellReadOptions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		delimiter byte
		timeout   *time.Duration
		raw       bool
		enhanced  bool
		wantError string
	}{
		{name: "ordinary options pass through", args: []string{"-n", "2", "value"}},
		{name: "prompt is not an option", args: []string{"-p", "-test", "value"}},
		{name: "grouped prompt is not an option", args: []string{"-rp", "-test", "value"}},
		{name: "identifier ends options", args: []string{"value", "-t"}},
		{name: "end of options", args: []string{"--", "-d"}},
		{name: "separate delimiter", args: []string{"-r", "-d", "", "value"}, delimiter: 0, raw: true, enhanced: true},
		{name: "attached delimiter", args: []string{"-d:", "value"}, delimiter: ':', enhanced: true},
		{name: "separate timeout", args: []string{"-t", "0.5", "value"}, delimiter: '\n', timeout: new(500 * time.Millisecond), enhanced: true},
		{name: "attached timeout", args: []string{"-t0", "value"}, delimiter: '\n', timeout: new(time.Duration(0)), enhanced: true},
		{name: "combined options", args: []string{"-t1", "-r", "-d:", "value"}, delimiter: ':', timeout: new(time.Second), raw: true, enhanced: true},
		{name: "missing delimiter", args: []string{"-d"}, wantError: "read: -d: option requires an argument"},
		{name: "missing timeout", args: []string{"-t"}, wantError: "read: -t: option requires an argument"},
		{name: "unsupported before delimiter", args: []string{"-s", "-d", "", "value"}, wantError: `read: unsupported option "-s" with -d or -t`},
		{name: "unsupported after delimiter", args: []string{"-d", "", "-n", "2", "value"}, wantError: `read: unsupported option "-n" with -d or -t`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, names, enhanced, err := parseShellRead(tc.args)
			if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || enhanced != tc.enhanced {
				t.Fatalf("enhanced = %v, error = %v", enhanced, err)
			}
			if !enhanced {
				if !slices.Equal(names, tc.args) {
					t.Fatalf("ordinary arguments = %q, want %q", names, tc.args)
				}
				return
			}
			if opts.raw != tc.raw || opts.delimiter != tc.delimiter || !slices.Equal(names, []string{"value"}) {
				t.Fatalf("options = %+v, names = %q", opts, names)
			}
			if (opts.timeout == nil) != (tc.timeout == nil) || opts.timeout != nil && *opts.timeout != *tc.timeout {
				t.Fatalf("timeout = %v, want %v", opts.timeout, tc.timeout)
			}
		})
	}
}

func TestShellReadOrdinaryPromptAndTimedNUL(t *testing.T) {
	for _, tc := range []struct {
		name, script, input, want string
	}{
		{"ordinary prompt", `read -p '-test' value; status=$?; printf '%s|%s' "$value" "$status"`, "hello\n", "-testhello|0"},
		{"timed NUL", `IFS= read -r -t 1 value; status=$?; printf '%s|%s' "$value" "$status"`, "a\x00b\n", "ab|0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, status := runShellWorkerTest(t, sharedProxyTestRegistry(t), "bash", nil, tc.script, shellReadInput(t, tc.input))
			if stdout != tc.want || stderr != "" || status != 0 {
				t.Fatalf("read = (%q, %q, %d), want stdout %q", stdout, stderr, status, tc.want)
			}
		})
	}
}

func TestShellReadWorkerPreservesLocalScopeAndNULLoop(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	stdin := shellReadInput(t, "one\x00two words\x00")
	script := `f() {
  local keep=local
  local IFS=:
  while IFS= read -r -d '' item; do
    printf '%s|%s|%s\n' "$item" "$keep" "$IFS"
  done
}
f`
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, script, stdin)
	if status != 0 || stderr != "" || stdout != "one|local|:\ntwo words|local|:\n" {
		t.Fatalf("worker read: stdout=%q stderr=%q status=%d", stdout, stderr, status)
	}
}

func TestShellReadDelimiterPreservesVariablesAndIFS(t *testing.T) {
	stdin := shellReadInput(t, " first:second\x00tail")
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(
		interp.StdIO(stdin, &stdout, &stderr),
		interp.ExecHandler(func(ctx context.Context, args []string) error {
			if args[0] == "xread" {
				args[0] = "read"
				return executeShellRead(ctx, args)
			}
			return interp.DefaultExecHandler(2)(ctx, args)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(`IFS=:; keep=old; xread -r -d '' left right; printf '%s|%s|%s' "$left" "$right" "$keep"`), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), file); err != nil {
		t.Fatalf("run: %v; stderr=%s", err, &stderr)
	}
	if got := stdout.String(); got != " first|second|old" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestShellReadTimeoutAssignsPartialInput(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { readFile.Close(); writeFile.Close() })
	if _, err := writeFile.WriteString("partial"); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	runner, err := interp.New(
		interp.StdIO(readFile, &stdout, &stdout),
		interp.ExecHandler(func(ctx context.Context, args []string) error {
			args[0] = "read"
			return executeShellRead(ctx, args)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(`xread -r -t 0.02 value; status=$?; printf '%s|%s' "$value" "$status"`), "")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := runner.Run(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
	if got := stdout.String(); got != "partial|142" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestShellReadEscapedDelimiter(t *testing.T) {
	stdin := shellReadInput(t, `one\:two:tail`)
	ctx := shellReadHandlerContext(t, stdin)
	if err := executeShellRead(ctx, []string{"read", "-d", ":", "value"}); err != nil {
		t.Fatal(err)
	}
	// Assignment is verified by the delimiter position: the escaped first colon
	// stays in the value, so reading resumes after the second colon.
	var rest [4]byte
	if _, err := stdin.Read(rest[:]); err != nil || string(rest[:]) != "tail" {
		t.Fatalf("remaining input = %q, err=%v", rest, err)
	}
}

func TestShellReadRejectsInvalidTimeoutBeforeReading(t *testing.T) {
	for _, timeout := range []string{"NaN", "+Inf", "1e100"} {
		stdin := shellReadInput(t, "value\x00")
		ctx := shellReadHandlerContext(t, stdin)
		err := executeShellRead(ctx, []string{"read", "-d", "", "-t", timeout, "value"})
		if status := shellReadExitStatus(err); status != 2 {
			t.Fatalf("timeout %q: status = %d, err=%v", timeout, status, err)
		}
		var one [1]byte
		if _, err := stdin.Read(one[:]); err != nil || one[0] != 'v' {
			t.Fatalf("timeout %q consumed stdin: byte=%q err=%v", timeout, one[0], err)
		}
	}
}

func TestShellReadCancellationInterruptsUntimedRead(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { readFile.Close(); writeFile.Close() })
	ctx, cancel := context.WithCancel(shellReadHandlerContext(t, readFile))
	done := make(chan error, 1)
	go func() { done <- executeShellRead(ctx, []string{"read", "-d", "", "value"}) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("read remained blocked after cancellation")
	}
}

func TestShellReadConcurrentTimeoutsAreIndependent(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { readFile.Close(); writeFile.Close() })
	shortCtx := shellReadHandlerContext(t, readFile)
	longCtx := shellReadHandlerContext(t, readFile)
	shortDone := make(chan error, 1)
	longDone := make(chan error, 1)
	go func() {
		shortDone <- executeShellRead(shortCtx, []string{"read", "-r", "-d", "", "-t", "0.02", "short"})
	}()
	time.Sleep(5 * time.Millisecond)
	go func() { longDone <- executeShellRead(longCtx, []string{"read", "-r", "-d", "", "-t", "0.5", "long"}) }()
	select {
	case err := <-shortDone:
		if shellReadExitStatus(err) != 142 {
			t.Fatalf("short read status = %d, err=%v", shellReadExitStatus(err), err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("short timeout was overwritten by concurrent read")
	}
	if _, err := writeFile.Write([]byte{'o', 'k', 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-longDone:
		if err != nil {
			t.Fatalf("long read: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("long read remained blocked")
	}
}

func TestShellReadInvalidIdentifierDoesNotConsume(t *testing.T) {
	stdin := shellReadInput(t, "value\x00")
	ctx := shellReadHandlerContext(t, stdin)
	err := executeShellRead(ctx, []string{"read", "-d", "", "bad-name"})
	if status := shellReadExitStatus(err); status != 2 {
		t.Fatalf("status = %d, err=%v", status, err)
	}
	var one [1]byte
	if _, err := stdin.Read(one[:]); err != nil || one[0] != 'v' {
		t.Fatalf("stdin consumed: byte=%q err=%v", one[0], err)
	}
}

func shellReadInput(t *testing.T, content string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "read-input")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func shellReadHandlerContext(t *testing.T, stdin *os.File) context.Context {
	t.Helper()
	var captured context.Context
	runner, err := interp.New(
		interp.StdIO(stdin, nil, nil),
		interp.ExecHandler(func(ctx context.Context, _ []string) error {
			captured = ctx
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader("capture"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	return captured
}

func shellReadExitStatus(err error) int {
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status)
	}
	return 0
}
