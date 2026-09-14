package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

func TestHRunCapture(t *testing.T) {
	t.Parallel()
	for _, tail := range []bool{false, true} {
		for _, chunks := range [][]string{
			{}, {""}, {"abc"}, {"12345678"}, {"123456789012"},
			{"abc", "def", "ghi", "jkl", "mn"}, {"123456789012", "x", "yz"},
			{"1234567", "ab", "12345678", "z"},
		} {
			capture := hrunCapture{buffer: make([]byte, 8), tail: tail}
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

func TestShellRunnerHRun(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, interpreter := range []string{"bash", "sh"} {
		t.Run(interpreter, func(t *testing.T) {
			t.Parallel()
			invocation := newShellWorkerTestInvocation(t.TempDir())
			stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil,
				`hrun --max-tokens 100 -- sh -c 'printf "stdout\n"; printf "stderr\n" >&2; exit 7'`, nil, invocation)
			if stdout != "stdout\n" || stderr != "stderr\n" || status != 7 {
				t.Fatalf("streams/status: %q, %q, %d", stdout, stderr, status)
			}

			stdout, stderr, status = runShellWorkerTest(t, registry, interpreter, nil,
				`mkdir 'child dir'
cd 'child dir'
printf 'stdin' | HRUN_TEST_VALUE='exported value' hrun --max-tokens 100 -- sh -c 'printf "%s:%s:%s:" "${PWD##*/}" "$HRUN_TEST_VALUE" "$1"; cat' sh 'quoted argument'`, nil, invocation)
			if stdout != "child dir:exported value:quoted argument:stdin" || stderr != "" || status != 0 {
				t.Fatalf("context: %q, %q, %d", stdout, stderr, status)
			}
		})
	}
}

func TestShellRunnerHRunDrainsBeyondDisplayAndHostBudgets(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	invocation := newShellWorkerTestInvocation(directory)
	for _, mode := range []string{"", "--tail"} {
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
			`hrun --max-tokens 32 `+mode+` -- sh -c 'printf "first-marker\n"; head -c 17000000 /dev/zero; printf "\nlast-marker\n"; printf "error\n" >&2; printf completed > completed; exit 7'`, nil, invocation)
		if status != 7 || !strings.Contains(stderr, "hrun: output incomplete: 32-token limit reached") {
			t.Fatalf("mode %q: stdout=%q stderr=%q status=%d", mode, stdout, stderr, status)
		}
		if mode == "" && !strings.HasPrefix(stdout, "first-marker\n") {
			t.Fatalf("head output = %q", stdout)
		}
		if mode != "" && !strings.HasSuffix(stdout, "\nlast-marker\n") {
			t.Fatalf("tail output = %q", stdout)
		}
		codec, err := tokenizer.ForModel(tokenizer.GPT5)
		if err != nil {
			t.Fatal(err)
		}
		outTokens, err := codec.Count(stdout)
		if err != nil {
			t.Fatal(err)
		}
		errTokens, err := codec.Count(strings.Split(stderr, "hrun:")[0])
		if err != nil || outTokens+errTokens > 32 {
			t.Fatalf("shared budget: %d + %d, %v", outTokens, errTokens, err)
		}
		if data, err := os.ReadFile(filepath.Join(directory, "completed")); err != nil || string(data) != "completed" {
			t.Fatalf("child did not finish: %q, %v", data, err)
		}
		if err := os.Remove(filepath.Join(directory, "completed")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShellRunnerHRunPrioritizesStderrAndPreservesSuccess(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	stdout, stderr, status := runShellWorkerTest(t, registry, "sh", nil,
		`hrun --tail --max-tokens 1 -- sh -c 'printf stdout; printf "first error" >&2'`, nil)
	if stdout != "" || !strings.HasPrefix(stderr, " error\nhrun: output incomplete:") || status != 0 {
		t.Fatalf("shared budget: %q, %q, %d", stdout, stderr, status)
	}
}

func TestShellRunnerHRunRejectsBeforeExecution(t *testing.T) {
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
			"hrun "+flags+" -- touch executed", nil, invocation)
		if status != 2 || !strings.Contains(stderr, "hrun:") {
			t.Fatalf("flags %q: stderr=%q status=%d", flags, stderr, status)
		}
		if _, err := os.Stat(filepath.Join(directory, "executed")); !os.IsNotExist(err) {
			t.Fatalf("invalid invocation executed a command: %v", err)
		}
	}
	for _, command := range []string{"hrun --max-tokens 10 --", "hrun --max-tokens 10 echo done"} {
		_, _, status := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
		if status != 2 {
			t.Fatalf("%q status = %d", command, status)
		}
	}
	_, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		fmt.Sprintf("hrun --max-tokens 100 -- %s", filepath.Join(directory, "missing")), nil, invocation)
	if status != 127 || stderr == "" {
		t.Fatalf("missing command: %q, %d", stderr, status)
	}
}

func TestShellRunnerHRunLargeSinglePiece(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		`hrun --max-tokens 1000 --tail -- sh -c 'head -c 20000 /dev/zero | tr "\000" a'`, nil)
	if status != 0 || (len(stdout)+7)/8 != 1000 || strings.Trim(stdout, "a") != "" ||
		!strings.Contains(stderr, "hrun: output incomplete") {
		t.Fatalf("long output: bytes=%d stderr=%q status=%d", len(stdout), stderr, status)
	}
}

func TestHRunLineCapture(t *testing.T) {
	t.Parallel()
	for _, tail := range []bool{false, true} {
		for _, source := range []string{"", "a", "a\n", "a\nb", "a\nb\n", "a\nb\nc", "\n\n\n", "α\r\nβ\nγ"} {
			capture := hrunCapture{maxLines: 2, tail: tail}
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

func TestShellRunnerHRunLines(t *testing.T) {
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
				"hrun "+tc.flags+" -- seq 1 1000", nil)
			if stdout != tc.want || status != 0 || !strings.Contains(stderr, "2-line limit") {
				t.Fatalf("%s: %q %q status=%d", tc.flags, stdout, stderr, status)
			}
		})
	}
	t.Run("line streams", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, status := runShellWorkerTest(t, registry, "sh", nil,
			`hrun -n 1 -- sh -c 'printf "out\nextra\n"; printf "err\nextra\n" >&2; exit 7'`, nil)
		if stdout != "out\n" || !strings.HasPrefix(stderr, "err\nhrun:") || status != 7 {
			t.Fatalf("line streams/status: %q %q %d", stdout, stderr, status)
		}
	})
	for _, mode := range []string{"", "--tail"} {
		t.Run("large "+mode, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
				`hrun -n 1 --max-tokens 20 `+mode+` -- sh -c 'printf START; head -c 17000000 /dev/zero | tr "\000" a; printf END'`, nil)
			if status != 0 || !strings.Contains(stderr, "20-token limit") ||
				(mode == "" && !strings.HasPrefix(stdout, "START")) ||
				(mode != "" && !strings.HasSuffix(stdout, "END")) {
				t.Fatalf("large selected line mode=%q: %q %q %d", mode, stdout, stderr, status)
			}
		})
	}
	t.Run("line-only default", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
			`hrun -n 1 -- sh -c 'head -c 200000 /dev/zero | tr "\000" a'`, nil)
		if status != 0 {
			t.Fatalf("line-only default: %d bytes %q %d", len(stdout), stderr, status)
		}
		retainedStdout, retainedStderr := retainedShellTestOutput(t, stderr)
		if len(stdout)+len(retainedStdout) != 200000 || retainedStderr != "" {
			t.Fatalf("outer display budget lost line-only output: %d bytes %q", len(retainedStdout), retainedStderr)
		}
	})

	for _, flags := range []string{"-n", "-n 0", "-n -1", "-n 01", "-n 1.5", "-n 999999999999999999999", "-n 1 -n 2"} {
		if _, _, err := parseHRunArguments(strings.Fields(flags + " -- echo")); err == nil {
			t.Fatalf("accepted %q", flags)
		}
	}
}

func TestHRunCombinedCaptureBoundsPendingLine(t *testing.T) {
	t.Parallel()
	for _, tail := range []bool{false, true} {
		capture := hrunCapture{maxLines: 2, buffer: make([]byte, 132), tail: tail}
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
