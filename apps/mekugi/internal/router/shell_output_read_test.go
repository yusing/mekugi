package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func (s *mekugiReplayStore) putShellOutput(ctx context.Context, stdout, stderr string, exitCode int) (string, error) {
	return s.publishOutput(ctx, command.OmittedOutput{Stdout: stdout, Stderr: stderr}, exitCode)
}

func TestShellOutputReadPagesAndRestart(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	replayDirectory := registry.ReplayDirectory
	store, err := openMekugiReplayStore(replayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	out, diagnostic := strings.Repeat("out π🙂\n", 36), strings.Repeat("err 引用\n", 30)
	id, err := store.putShellOutput(t.Context(), out, diagnostic, 7)
	if err != nil {
		t.Fatal(err)
	}

	// Keep sh represented and exercise the executable frontend's
	// restart/continuation behavior under a changed cwd, thread ID and state directory.
	stdout, stderr, status := runShellWorkerTest(t, registry, "sh", nil, "mread "+id+" --stdout --max-tokens 48", nil)
	if status != 1 || stdout == "" || !strings.Contains(stderr, "next_call: mread ") {
		t.Fatalf("sh boundary: %d %q %q", status, stdout, stderr)
	}
	invocation := newShellWorkerTestInvocation(t.TempDir(), "XDG_STATE_HOME="+t.TempDir())
	first, firstErr, firstStatus := runShellWorkerTest(t, registry, "bash", nil,
		"mread "+id+" --stderr --max-tokens 48", nil, invocation)
	const notice = "read: incomplete; next_call: mread "
	if firstStatus != 1 || !strings.Contains(first, "err 引用") || strings.Contains(first, "out π") || !strings.HasPrefix(firstErr, notice) {
		t.Fatalf("restart boundary: %d %q %q", firstStatus, first, firstErr)
	}
	cursor := strings.Fields(strings.TrimPrefix(firstErr, notice))[0]
	command := "mread " + cursor + " --max-tokens 48"
	next, nextErr, nextStatus := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
	again, againErr, againStatus := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
	if next != again || nextErr != againErr || nextStatus != againStatus {
		t.Fatalf("cursor read consumed or changed output: first=%q/%q/%d again=%q/%q/%d",
			next, nextErr, nextStatus, again, againErr, againStatus)
	}
	if !strings.Contains(next, "err 引用") || strings.Contains(next, "out π") || nextStatus != 1 || !strings.HasPrefix(nextErr, notice) {
		t.Fatalf("continued stderr binding failed: %d %q %q", nextStatus, next, nextErr)
	}
}

func TestFileAndOutlineReadRecoveryAfterSourceRemoval(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, command := range []string{"mcat", "inspect_file"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			source := filepath.Join(directory, "sample.go")
			var content strings.Builder
			content.WriteString("package p\n")
			for i := range 24 {
				fmt.Fprintf(&content, "func Item%d() {}\n", i)
			}
			if err := os.WriteFile(source, []byte(content.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			invocation := newShellWorkerTestInvocation(directory)
			readCommand := command
			if command == "inspect_file" {
				readCommand += " --json"
			}
			full, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
				readCommand+" "+source+" --max-tokens 15500", nil, invocation)
			if status != 0 || diagnostic != "" {
				t.Fatalf("full read: %d %q", status, diagnostic)
			}
			budget := "256"
			if command == "mcat" {
				budget = "64"
			}
			first, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
				readCommand+" "+source+" --max-tokens "+budget, nil, invocation)
			if status == 0 || first == "" {
				t.Fatalf("expected partial output: %d %q %q", status, first, diagnostic)
			}
			reference := func(diagnostic string) string {
				t.Helper()
				_, ref, found := strings.Cut(diagnostic, "next_call: mread ")
				if !found {
					t.Fatalf("missing continuation: %q", diagnostic)
				}
				return strings.Fields(ref)[0]
			}
			ref := reference(diagnostic)
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			// A stream-only read must not consume the other stream or reopen the source.
			empty, diagnostic, status := runShellWorkerTest(t, registry, "sh", nil,
				"mread "+ref+" --stderr", nil, invocation)
			if status != 0 || empty != "" || diagnostic != "" {
				t.Fatalf("stderr selection: %d %q %q", status, empty, diagnostic)
			}
			rows := first
			var entries []json.RawMessage
			if command == "inspect_file" {
				var initial struct {
					Data struct{ Outline []json.RawMessage }
				}
				if err := json.Unmarshal([]byte(first), &initial); err != nil {
					t.Fatal(err)
				}
				entries = initial.Data.Outline
			}
			complete := false
			for pageIndex := range 100 {
				page, diagnostic, status := runShellWorkerTest(t, registry, "sh", nil,
					"mread "+ref+" --max-tokens "+budget, nil, invocation)
				payload := page
				if command == "mcat" {
					var found bool
					_, payload, found = strings.Cut(payload, "\n")
					if !found || !strings.HasPrefix(page, "[rows ") {
						t.Fatalf("missing row range: %q", page)
					}
					if !strings.HasSuffix(payload, "\n") {
						t.Fatal("partial source row")
					}
					rows += payload
				} else {
					var next []json.RawMessage
					if err := json.Unmarshal([]byte(payload), &next); err != nil {
						t.Fatal(err)
					}
					entries = append(entries, next...)
				}
				if status == 0 {
					if pageIndex < 1 {
						t.Fatalf("fixture did not exercise continuation pagination: %d pages", pageIndex+1)
					}
					complete = true
					break
				}
				ref = reference(diagnostic)
			}
			if !complete {
				t.Fatal("read did not complete")
			}
			if command == "mcat" {
				if rows != full {
					t.Fatal("recovery lost or duplicated source rows")
				}
			} else {
				var expected struct {
					Data struct{ Outline []json.RawMessage }
				}
				if err := json.Unmarshal([]byte(full), &expected); err != nil {
					t.Fatal(err)
				}
				actualJSON, _ := json.Marshal(entries)
				expectedJSON, _ := json.Marshal(expected.Data.Outline)
				if string(actualJSON) != string(expectedJSON) {
					t.Fatal("recovery lost, changed, or duplicated outline entries")
				}
			}
		})
	}
}

func TestMSymbolFrontendRecoveryAfterSourceRemoval(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "sample.go")
	var content strings.Builder
	content.WriteString("package p\nfunc Target() {}\n")
	for index := range 80 {
		fmt.Fprintf(&content, "func Use%d() { Target() }\n", index)
	}
	if err := os.WriteFile(source, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	resolverDirectory := installMSymbolRecoveryLSP(t, source)
	invocation := newShellWorkerTestInvocation(directory,
		"PATH="+resolverDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	full, diagnostic, status := runShellWorkerTest(t, registry, "sh", nil,
		"msymbol --max-tokens 15500 refs sample.go 2 Target", nil, invocation)
	if status != 0 || diagnostic != "" ||
		!strings.HasPrefix(full, "\"sample.go\":\n3 func Use0() { Target() }\n") {
		t.Fatalf("full symbol read: %d %q %q", status, full, diagnostic)
	}
	first, diagnostic, status := runShellWorkerTest(t, registry, "sh", nil,
		"msymbol --max-tokens 16 refs sample.go 2 Target", nil, invocation)
	if status == 0 || !strings.Contains(diagnostic, "msymbol: output incomplete") {
		t.Fatalf("partial symbol read: %d %q %q", status, first, diagnostic)
	}
	_, reference, found := strings.Cut(diagnostic, "read: incomplete; next_call: mread ")
	if !found {
		t.Fatalf("missing symbol continuation: %q", diagnostic)
	}
	reference = strings.Fields(reference)[0]
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	recovered := first
	for range 100 {
		page, next, pageStatus := runShellWorkerTest(t, registry, "sh", nil,
			"mread "+reference+" --max-tokens 256", nil, invocation)
		if page != "" && !strings.HasSuffix(page, "\n") {
			t.Fatal("partial symbol row")
		}
		recovered += page
		if pageStatus == 0 {
			if recovered != full {
				t.Fatal("symbol recovery lost or duplicated rows")
			}
			return
		}
		_, reference, found = strings.Cut(next, "read: incomplete; next_call: mread ")
		if !found {
			t.Fatalf("missing next symbol continuation: %q", next)
		}
		reference = strings.Fields(reference)[0]
	}
	t.Fatal("symbol recovery did not complete")
}

func TestOutlineRecoveryCapacityPreservesCurrentOutput(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "large.md")
	if err := os.WriteFile(source, []byte(strings.Repeat("# "+strings.Repeat("x", 70_000)+"\n", 250)), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"inspect_file --json "+source+" --max-tokens 256", nil, newShellWorkerTestInvocation(directory))
	if status != 1 || !json.Valid([]byte(stdout)) || !strings.Contains(stdout, `"truncated":true`) ||
		!strings.Contains(stderr, "recovery unavailable") || strings.Contains(stderr, "next_call") {
		t.Fatalf("capacity discarded valid output or advertised recovery: %d %q %q", status, stdout, stderr)
	}
}

func TestMReadKeepsValidTargetsAroundMissingHandle(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	replayDirectory := registry.ReplayDirectory
	store, err := openMekugiReplayStore(replayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.putShellOutput(t.Context(), "first-success\n", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	last, err := store.putShellOutput(t.Context(), "last-success\n", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	result := executeRecovery(t.Context(), replayDirectory, []string{first, "amber999999", last})
	if result.ExitCode != 1 || !strings.Contains(result.Stdout, "first-success") || !strings.Contains(result.Stdout, "last-success") || !strings.Contains(result.Stderr, "amber999999") {
		t.Fatalf("mixed read: %+v", result)
	}
}
