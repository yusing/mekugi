package frontends

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/fileread/read"
)

func TestFrontendRecoveryFailurePreservesAdmittedOutput(t *testing.T) {
	registry := sharedTestRegistry(t)
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
			handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), commands, t.Context(), filepath.Join(registry.FrontendDirectory, "mrun"), nil, nil, &stdout, &stderr)
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
	registry := sharedTestRegistry(t)
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
	t.Setenv(evidence.AXReadOutputEnvironment, log)
	var stdout, stderr bytes.Buffer
	handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), t.Context(), filepath.Join(registry.FrontendDirectory, "mcat"),
		[]string{"--max-tokens", "2000", path, filepath.Join(directory, "missing")}, nil, &stdout, &stderr)
	if !handled || status != 1 || !strings.Contains(stdout.String(), body) || !strings.Contains(stdout.String(), "status=failed") {
		t.Fatalf("handled=%t status=%d stdout=%q stderr=%q", handled, status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "ENOENT") || !strings.Contains(stderr.String(), "mcat: recovery unavailable:") || !strings.Contains(stderr.String(), "encoded read record") || strings.Contains(stdout.String()+stderr.String(), "next_call") {
		t.Fatalf("publication failure lost diagnostics or exposed reference: %q", stderr.String())
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	if count, err := codec.Count(stdout.String()); err != nil || count > 2000 {
		t.Fatalf("mcat output uses %d tokens, budget=2000 err=%v", count, err)
	}
	reads, err := new(evidence.Service).ReadReads(t.Context(), log, thread)
	if err != nil || reads.Started != 1 || reads.Completed != 1 || reads.Failed != 1 || reads.Succeeded != 0 {
		t.Fatalf("read evidence=%+v err=%v", reads, err)
	}
}

func TestFrontendBundleCanceledBeforePublication(t *testing.T) {
	registry := sharedTestRegistry(t)
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
	handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), commands, ctx, filepath.Join(registry.FrontendDirectory, "mcat"), []string{"--max-tokens", "1000", path, path}, nil, &stdout, &stderr)
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
		got, err := retainExecutionOutput(t.Context(), nil, scope.Retained{}, output)
		if err != nil || got != output {
			t.Fatalf("complete bundle needed a recovery store: output=%+v err=%v", got, err)
		}
	}
}

func TestFrontendMultiFileRecoverySurvivesHostDeliveryCeiling(t *testing.T) {
	registry := sharedTestRegistry(t)
	t.Setenv(codexThreadIDEnvironment, uniqueTestThread("host-delivery-budget"))
	directory := t.TempDir()
	want := make(map[string]string)
	var allSource strings.Builder
	args := []string{"--max-tokens", "1000"}
	for _, name := range []string{"left", "right"} {
		var source strings.Builder
		for i := range 360 {
			key := fmt.Sprintf("%s-%03d", name, i)
			row := key + " " + strings.Repeat("representation ", 4) + "\n"
			want[key] = row
			source.WriteString(row)
		}
		path := filepath.Join(directory, name)
		allSource.WriteString(source.String())
		if err := os.WriteFile(path, []byte(source.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, path)
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	count, err := codec.Count(allSource.String())
	if err != nil || count >= 6400 {
		t.Fatalf("fixture must fit GPT admission before host framing: tokens=%d err=%v", count, err)
	}
	t.Logf("complete fixture: %d bytes, %d GPT tokens", allSource.Len(), count)
	seen := make(map[string]string)
	nextRow := make(map[string]int)
	name := "mcat"
	for page := range 10 {
		var stdout, stderr bytes.Buffer
		handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), t.Context(),
			filepath.Join(registry.FrontendDirectory, name), args, nil, &stdout, &stderr)
		// Codex utils/string truncation admits ceil(UTF-8 bytes/4) tokens.
		// Include frontend labels, row frames, diagnostics and the next call.
		delivered := stdout.String() + stderr.String()
		if !handled || (len(delivered)+3)/4 > 7000 {
			t.Fatalf("page %d would lose text at the host ceiling: bytes=%d status=%d", page, len(delivered), status)
		}
		if page == 1 {
			if (len(delivered)+3)/4 < 6000 || status != 1 {
				t.Fatalf("fixture did not exercise near-budget retained paging: bytes=%d status=%d", len(delivered), status)
			}
			t.Logf("first retained page: %d approximate host tokens, status=%d", (len(delivered)+3)/4, status)
		}
		for row := range strings.Lines(stdout.String()) {
			key, _, _ := strings.Cut(row, " ")
			if _, ok := want[key]; ok {
				prefix, _, _ := strings.Cut(key, "-")
				if key != fmt.Sprintf("%s-%03d", prefix, nextRow[prefix]) {
					t.Fatalf("source rows out of order: %s", key)
				}
				nextRow[prefix]++
				if _, duplicate := seen[key]; duplicate {
					t.Fatalf("duplicate row %s", key)
				}
				seen[key] = row
			}
		}
		_, next, incomplete := strings.Cut(delivered, "next_call: mread ")
		if !incomplete {
			if status != 0 || len(seen) != len(want) {
				t.Fatalf("completed with lost rows: seen=%d want=%d status=%d stderr=%s", len(seen), len(want), status, stderr.String())
			}
			for key, row := range want {
				if seen[key] != row {
					t.Fatalf("changed source row %s", key)
				}
			}
			return
		}
		next, _, _ = strings.Cut(next, "\n")
		args = strings.Fields(next)
		if page == 0 {
			// The regression starts with multiple retained files, not one page.
			if len(args) < 2 || strings.HasPrefix(args[1], "--") {
				t.Fatalf("missing multi-file continuation: %s", next)
			}
			args = args[:2]
		}
		// Every mread uses the reported near-budget request and host ceiling.
		for i, arg := range args {
			if arg == "--max-tokens" {
				args = args[:i]
				break
			}
		}
		args = append(args, "--max-tokens", "6500")
		name = "mread"
	}
	t.Fatal("retained reads did not finish")
}
