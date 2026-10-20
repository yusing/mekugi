package router

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func TestMRunFrontendPreservesHostStdinContinuation(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	frontend, ok := registry.frontends["mrun"]
	if !ok {
		t.Fatal("mrun session frontend is unavailable")
	}
	workingDirectory := t.TempDir()
	ready := filepath.Join(workingDirectory, "ready")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, frontend,
		"--max-tokens", "100", "--", "sh", "-c",
		"printf ready > ready; IFS= read -r value; printf 'continued:%s\\n' \"$value\"",
	)
	command.Dir = workingDirectory
	command.Env = append(os.Environ(), routerTestWorkerEnvironment+"=1")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("mrun exited before host continuation: %v, stderr %q", err, stderr.String())
		case <-ctx.Done():
			t.Fatal("mrun child did not become ready")
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case err := <-done:
		t.Fatalf("mrun exited before stdin continuation: %v, stderr %q", err, stderr.String())
	default:
	}
	if _, err := input.Write([]byte("host-input\n")); err != nil {
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil || stdout.String() != "continued:host-input\n" || stderr.String() != "" {
			t.Fatalf("mrun continuation: %v, stdout %q, stderr %q", err, stdout.String(), stderr.String())
		}
	case <-ctx.Done():
		t.Fatal("mrun did not finish after stdin continuation")
	}
}

func TestMRunCapture(t *testing.T) {
	t.Parallel()
	for _, tail := range []bool{false, true} {
		for _, chunks := range [][]string{
			{}, {""}, {"abc"}, {"12345678"}, {"123456789012"},
			{"abc", "def", "ghi", "jkl", "mn"}, {"123456789012", "x", "yz"},
			{"1234567", "ab", "12345678", "z"},
		} {
			capture := mrunCapture{buffer: make([]byte, 8), tail: tail}
			for _, chunk := range chunks {
				n, err := capture.Write([]byte(chunk))
				if err != nil || n != len(chunk) {
					t.Fatalf("write = %d, %v", n, err)
				}
			}
			all := strings.Join(chunks, "")
			want := all
			if len(want) > 8 {
				if tail {
					want = want[len(want)-8:]
				} else {
					want = want[:8]
				}
			}
			if got := capture.text(); got != want || capture.omitted != (len(all) > 8) {
				t.Fatalf("tail=%t chunks=%q: got %q, omitted=%t; want %q", tail, chunks, got, capture.omitted, want)
			}
		}
	}
}

func TestShellRunnerMRun(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, interpreter := range []string{"bash", "sh"} {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()
			invocation := newShellWorkerTestInvocation(t.TempDir())
			stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil,
				`mrun --max-tokens 100 -- sh -c 'printf "stdout\n"; printf "stderr\n" >&2; exit 7'`, nil, invocation)
			if stdout != "stdout\n" || stderr != "stderr\n" || status != 7 {
				t.Fatalf("streams/status: %q, %q, %d", stdout, stderr, status)
			}

			stdout, stderr, status = runShellWorkerTest(t, registry, interpreter, nil,
				`mkdir 'child dir'
cd 'child dir'
printf 'stdin' | MRUN_TEST_VALUE='exported value' mrun --max-tokens 100 -- sh -c 'printf "%s:%s:%s:" "${PWD##*/}" "$MRUN_TEST_VALUE" "$1"; cat' sh 'quoted argument'`, nil, invocation)
			if stdout != "child dir:exported value:quoted argument:stdin" || stderr != "" || status != 0 {
				t.Fatalf("context: %q, %q, %d", stdout, stderr, status)
			}
		})
	}
}

func TestShellRunnerMRunDrainsBeyondDisplayAndHostBudgets(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	invocation := newShellWorkerTestInvocation(directory)
	for _, mode := range []string{"", "--tail"} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
			`mrun --max-tokens 32 `+mode+` -- sh -c 'printf "first-marker\n"; head -c 17000000 /dev/zero; printf "\nlast-marker\n"; printf "error\n" >&2; printf completed > completed; exit 7'`, nil, invocation)
		if status != 7 || !strings.Contains(stderr, "mrun: output incomplete: 32-token limit reached") {
			t.Fatalf("mode %q: stdout=%q stderr=%q status=%d", mode, stdout, stderr, status)
		}
		if mode == "" && !strings.HasPrefix(stdout, "first-marker\n") {
			t.Fatalf("head output = %q", stdout)
		}
		if mode != "" && !strings.HasSuffix(stdout, "\nlast-marker\n") {
			t.Fatalf("tail output = %q", stdout)
		}
		codec, err := tokenizer.New()
		if err != nil {
			t.Fatal(err)
		}
		outTokens, err := codec.Count(stdout)
		if err != nil {
			t.Fatal(err)
		}
		errPayload, _, _ := strings.Cut(stderr, "mrun:")
		errTokens, err := codec.Count(errPayload)
		if err != nil || outTokens+errTokens > 32 {
			t.Fatalf("shared budget: %d + %d, %v", outTokens, errTokens, err)
		}
		if stdout != "" && errTokens > 16 {
			t.Fatalf("stderr consumed %d tokens of the 32-token shared budget while stdout was present", errTokens)
		}
		if data, err := os.ReadFile(filepath.Join(directory, "completed")); err != nil || string(data) != "completed" {
			t.Fatalf("child did not finish: %q, %v", data, err)
		}
		if err := os.Remove(filepath.Join(directory, "completed")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShellRunnerMRunBoundsStderrWhenStdoutIsPresentAndPreservesSuccess(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	stdout, stderr, status := runShellWorkerTest(t, registry, "sh", nil,
		`mrun --tail --max-tokens 1 -- sh -c 'printf stdout; printf "first error" >&2'`, nil)
	if stdout == "" || strings.Contains(stderr, "first error") ||
		!strings.Contains(stderr, "mrun: output incomplete: 1-token limit reached") || status != 0 {
		t.Fatalf("shared budget: %q, %q, %d", stdout, stderr, status)
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	stdoutTokens, err := codec.Count(stdout)
	if err != nil || stdoutTokens > 1 {
		t.Fatalf("stdout uses %d tokens for one-token budget: %v", stdoutTokens, err)
	}
}

func TestShellRunnerMRunRejectsBeforeExecution(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	invocation := newShellWorkerTestInvocation(directory)
	for _, flags := range []string{
		"", "--tail", "--max-tokens", "--max-tokens 0", "--max-tokens 15501",
		"--max-tokens -1", "--max-tokens 01", "--max-tokens 1e3",
		"--max-tokens 10 --max-tokens 20", "--max-tokens 10 --tail --tail",
		"--max-tokens 10 --preview-bytes 5",
	} {
		_, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
			"mrun "+flags+" -- touch executed", nil, invocation)
		if status != 2 || !strings.Contains(stderr, "mrun:") {
			t.Fatalf("flags %q: stderr=%q status=%d", flags, stderr, status)
		}
		if _, err := os.Stat(filepath.Join(directory, "executed")); !os.IsNotExist(err) {
			t.Fatalf("invalid invocation executed a command: %v", err)
		}
	}
	for _, command := range []string{"mrun --max-tokens 10 --", "mrun --max-tokens 10"} {
		_, _, status := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
		if status != 2 {
			t.Fatalf("%q status = %d", command, status)
		}
	}
	_, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		fmt.Sprintf("mrun --max-tokens 100 -- %s", filepath.Join(directory, "missing")), nil, invocation)
	if status != 127 || stderr == "" {
		t.Fatalf("missing command: %q, %d", stderr, status)
	}
}

func TestShellRunnerMRunLargeSinglePiece(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		`mrun --max-tokens 1000 --tail -- sh -c 'head -c 20000 /dev/zero | tr "\000" a'`, nil)
	if status != 0 || (len(stdout)+7)/8 != 1000 || strings.Trim(stdout, "a") != "" ||
		!strings.Contains(stderr, "mrun: output incomplete") {
		t.Fatalf("long output: bytes=%d stderr=%q status=%d", len(stdout), stderr, status)
	}
}

func TestMRunLineCapture(t *testing.T) {
	t.Parallel()
	for _, tail := range []bool{false, true} {
		for _, source := range []string{"", "a", "a\n", "a\nb", "a\nb\n", "a\nb\nc", "\n\n\n", "α\r\nβ\nγ"} {
			capture := mrunCapture{maxLines: 2, tail: tail}
			// Byte-at-a-time writes exercise split UTF-8 and newline boundaries.
			for index := range len(source) {
				if _, err := capture.Write([]byte(source[index : index+1])); err != nil {
					t.Fatal(err)
				}
			}
			lines := strings.SplitAfter(source, "\n")
			if lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			omitted := len(lines) > 2
			if omitted {
				if tail {
					lines = lines[len(lines)-2:]
				} else {
					lines = lines[:2]
				}
			}
			want := strings.Join(lines, "")
			if got := capture.text(); got != want || capture.omitted != omitted {
				t.Fatalf("tail=%t source=%q: got %q omitted=%t; want %q omitted=%t", tail, source, got, capture.omitted, want, omitted)
			}
		}
	}
}

func TestShellRunnerMRunLines(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, tc := range []struct{ flags, want string }{
		{"-n 2", "1\n2\n"},
		{"--tail -n 2", "999\n1000\n"},
		{"-n 2 --max-tokens 100", "1\n2\n"},
		{"--max-tokens 100 --tail -n 2", "999\n1000\n"},
	} {
		t.Run(tc.flags, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
				"mrun "+tc.flags+" -- seq 1 1000", nil)
			if stdout != tc.want || status != 0 || !strings.Contains(stderr, "2-line limit") {
				t.Fatalf("%s: %q %q status=%d", tc.flags, stdout, stderr, status)
			}
		})
	}
	t.Run("line streams", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, status := runShellWorkerTest(t, registry, "sh", nil,
			`mrun -n 1 -- sh -c 'printf "out\nextra\n"; printf "err\nextra\n" >&2; exit 7'`, nil)
		if stdout != "out\n" || !strings.HasPrefix(stderr, "err\nmrun:") || status != 7 {
			t.Fatalf("line streams/status: %q %q %d", stdout, stderr, status)
		}
	})
	for _, mode := range []string{"", "--tail"} {
		t.Run("large "+mode, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
				`mrun -n 1 --max-tokens 20 `+mode+` -- sh -c 'printf START; head -c 200000 /dev/zero | tr "\000" a; printf END'`, nil)
			if status != 0 || !strings.Contains(stderr, "20-token limit") ||
				(mode == "" && !strings.HasPrefix(stdout, "START")) ||
				(mode != "" && !strings.HasSuffix(stdout, "END")) {
				t.Fatalf("large selected line mode=%q: %q %q %d", mode, stdout, stderr, status)
			}
		})
	}
	t.Run("line-only default", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		handled, status := RunToolPluginWorker(t.Context(), registry.frontends["mrun"],
			[]string{"-n", "1", "--", "sh", "-c", `head -c 200000 /dev/zero | tr "\000" a`},
			nil, &stdout, &stderr)
		if !handled || status != 0 || stdout.Len() != 0 {
			t.Fatalf("line-only default: handled %t, %d bytes %q %d", handled, stdout.Len(), stderr.String(), status)
		}
		_, receipt, found := strings.Cut(stderr.String(), "next_call: mread ")
		if !found || len(strings.Fields(receipt)) == 0 {
			t.Fatalf("missing mrun recovery receipt: %q", stderr.String())
		}
		reference := strings.Fields(receipt)[0]
		var recovered bytes.Buffer
		for pageIndex := range 20 {
			var page, diagnostic bytes.Buffer
			handled, status = RunToolPluginWorker(t.Context(), registry.frontends["mread"],
				[]string{reference, "--max-tokens", "15500"}, nil, &page, &diagnostic)
			if !handled || status != 0 && status != 1 || strings.Trim(page.String(), "a") != "" {
				t.Fatalf("mrun recovery page %d: handled %t, bytes %d, stderr %q, status %d",
					pageIndex, handled, page.Len(), diagnostic.String(), status)
			}
			recovered.Write(page.Bytes())
			if status == 0 {
				if diagnostic.Len() != 0 {
					t.Fatalf("completed mrun recovery has diagnostics: %q", diagnostic.String())
				}
				break
			}
			_, next, found := strings.Cut(diagnostic.String(), "next_call: mread ")
			if !found || len(strings.Fields(next)) == 0 {
				t.Fatalf("missing mread continuation on page %d: %q", pageIndex, diagnostic.String())
			}
			reference = strings.Fields(next)[0]
			if pageIndex == 19 {
				t.Fatal("mrun line-only recovery exceeded its page bound")
			}
		}
		if recovered.Len() != 200000 || strings.Trim(recovered.String(), "a") != "" {
			t.Fatalf("mrun recovery: visible %d, retained %d", stdout.Len(), recovered.Len())
		}
	})

	for _, flags := range []string{"-n", "-n 0", "-n -1", "-n 01", "-n 1.5", "-n 999999999999999999999", "-n 1 -n 2"} {
		if _, _, err := parseMRunArguments(strings.Fields(flags + " -- echo")); err == nil {
			t.Fatalf("accepted %q", flags)
		}
	}
}

func TestMRunCombinedCaptureBoundsPendingLine(t *testing.T) {
	t.Parallel()
	for _, tail := range []bool{false, true} {
		capture := mrunCapture{maxLines: 2, buffer: make([]byte, 132), tail: tail}
		chunk := []byte(strings.Repeat("a", 4096))
		for range 1000 {
			capture.Write(chunk)
			if capture.pending.Len() != 0 || capture.pendingBytes == nil ||
				capture.pendingBytes.size > len(capture.buffer) {
				t.Fatal("combined mode retained an unbounded pending line")
			}
		}
		capture.Write([]byte("END\nlast\n"))
		got := capture.text()
		if !capture.omitted || len(got) > len(capture.buffer) {
			t.Fatalf("tail=%t: bytes=%d omitted=%t", tail, len(got), capture.omitted)
		}
		if tail && !strings.HasSuffix(got, "END\nlast\n") {
			t.Fatalf("tail lost final line boundaries: %q", got)
		}
		if !tail && strings.Trim(got, "a") != "" {
			t.Fatalf("head lost initial bytes: %q", got)
		}
	}
}
