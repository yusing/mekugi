package router

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/capturer"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/fileread/read"
)

func TestFrontendRecoveryFailurePreservesAdmittedOutput(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	// The raw omissions fit, but JSON escaping exceeds the encoded-record bound.
	omitted := strings.Repeat("\x01", 6<<20) + "\n"
	for _, originalStatus := range []int{0, 23} {
		t.Run(strconv.Itoa(originalStatus), func(t *testing.T) {
			commands := slices.Clone(testCommands())
			const admitted = "admitted command rows\n"
			const diagnostic = "original diagnostic"
			for i := range commands {
				if commands[i].Name == "mrun" {
					commands[i].Run = func(context.Context, command.Request) (command.Output, error) {
						return command.Output{Stdout: admitted, Stderr: diagnostic, ExitCode: originalStatus, OmittedOutput: &command.OmittedOutput{Stdout: omitted, StdoutKind: "rows"}}, nil
					}
				}
			}
			var stdout, stderr bytes.Buffer
			handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), commands, t.Context(), registry.frontends["mrun"], nil, nil, &stdout, &stderr)
			wantStatus := originalStatus
			if wantStatus == 0 {
				wantStatus = 1
			}
			if !handled || status != wantStatus || stdout.String() != admitted {
				t.Fatalf("handled=%t status=%d stdout=%q stderr=%q", handled, status, stdout.String(), stderr.String())
			}
			if !strings.HasPrefix(stderr.String(), diagnostic+"\nmrun: recovery unavailable: ") || !strings.Contains(stderr.String(), "encoded read record") || strings.Contains(stderr.String(), "next_call") {
				t.Fatalf("failed publication diagnostics: %q", stderr.String())
			}
		})
	}
}

func TestFrontendBundleRecoveryFailurePreservesBodiesAndReadEvidence(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "rows")
	const body = "admitted source row\n"
	// Each row fits the scan bound; the omitted JSON record exceeds its bound.
	source := body + strings.Repeat(strings.Repeat("\x01", 1024)+"\n", 6144)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(directory, "reads.jsonl")
	thread := uniqueTestThread("bundle-publication-failure")
	t.Setenv(codexThreadIDEnvironment, thread)
	t.Setenv(capturer.AXReadOutputEnvironment, log)
	var stdout, stderr bytes.Buffer
	handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), t.Context(), registry.frontends["mcat"],
		[]string{"--max-tokens", "2000", path, filepath.Join(directory, "missing")}, nil, &stdout, &stderr)
	if !handled || status != 1 || !strings.Contains(stdout.String(), body) || !strings.Contains(stdout.String(), "status=failed") {
		t.Fatalf("handled=%t status=%d stdout=%q stderr=%q", handled, status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "ENOENT") || !strings.Contains(stderr.String(), "mcat: recovery unavailable:") || !strings.Contains(stderr.String(), "encoded read record") || strings.Contains(stdout.String()+stderr.String(), "next_call") {
		t.Fatalf("publication failure lost diagnostics or exposed reference: %q", stderr.String())
	}
	assertMCatOutputWithinBudget(t, 2000, stdout.String(), "")
	reads, err := capturer.ReadAXReads(t.Context(), log, thread)
	if err != nil || reads.Started != 1 || reads.Completed != 1 || reads.Failed != 1 || reads.Succeeded != 0 {
		t.Fatalf("read evidence=%+v err=%v", reads, err)
	}
}

func TestFrontendBundleCanceledBeforePublication(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "rows")
	if err := os.WriteFile(path, []byte(strings.Repeat("admitted source row alpha beta gamma\n", 500)), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	commands := slices.Clone(testCommands())
	for i := range commands {
		if commands[i].Name == "mcat" {
			run := commands[i].Run
			commands[i].Run = func(ctx context.Context, request command.Request) (command.Output, error) {
				out, err := run(ctx, request)
				if err != nil || out.Delivery == nil {
					t.Fatalf("no pending bundle: %+v %v", out, err)
				}
				cancel()
				return out, err
			}
		}
	}
	var stdout, stderr bytes.Buffer
	handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), commands, ctx, registry.frontends["mcat"], []string{"--max-tokens", "1000", path, path}, nil, &stdout, &stderr)
	if !handled || status != 1 || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("handled=%t status=%d stderr=%q", handled, status, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("canceled invocation delivered %d bytes of source output", stdout.Len())
	}
}

func TestFrontendCompleteBundlesNeedNoRecoveryStore(t *testing.T) {
	directory := t.TempDir()
	full, empty, missing := filepath.Join(directory, "full"), filepath.Join(directory, "empty"), filepath.Join(directory, "missing")
	for path, content := range map[string]string{full: "source row\n", empty: ""} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{full, full}, {empty, empty}, {missing, missing}} {
		output, err := (read.Service{}).Execute(t.Context(), command.Request{Args: args, Stdin: nil})
		if err != nil || output.Delivery != nil || output.OmittedOutput != nil {
			t.Fatalf("output=%+v err=%v", output, err)
		}
		got, err := retainExecutionOutput(t.Context(), toolWorkerManifest{}, output)
		if err != nil || got != output {
			t.Fatalf("complete bundle needed a recovery store: output=%+v err=%v", got, err)
		}
	}
}
