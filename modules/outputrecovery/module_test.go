package outputrecovery_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/reference"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/outputrecovery"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func composedRecovery(t *testing.T, directory string) (*recovery.Service, command.Contribution, scope.Retained, *history.Store) {
	t.Helper()
	kinds := outputrecovery.Module.Contributes.RecordKinds
	owner := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{RecordKinds: kinds}, nil)
	store, err := owner.Open(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	selected := scope.Retained{Directory: directory, Namespace: "requesting-thread", Thread: "requesting-thread"}
	script := new(moduletest.Scripted)
	service := moduletest.New(t, outputrecovery.Module, outputrecovery.Deps{History: owner}, module.Slots{}, script)
	definitions, registered := outputrecovery.Module.Contributes.Commands, script.Commands()
	if outputrecovery.Module.Lifetime != module.Stateless || len(definitions) != 1 || len(registered) != 1 {
		t.Fatalf("definitions=%v registered=%v", definitions, registered)
	}
	executable := moduletest.BindCommand(definitions[0], service)
	if executable.Name != "mread" || executable.Run == nil || executable.Spec != registered[0] {
		t.Fatalf("registered command=%+v", executable)
	}
	if err := executable.Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	return service, executable, selected, store
}

func publish(t *testing.T, service *recovery.Service, selected scope.Retained, output command.OmittedOutput) string {
	t.Helper()
	session, err := service.Attach(selected, recovery.DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := session.Publish(t.Context(), output, 23)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func execute(t *testing.T, executable command.Contribution, selected scope.Retained, args ...string) command.Output {
	t.Helper()
	output, err := executable.Run(t.Context(), command.Request{Scope: selected, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func continuation(t *testing.T, output command.Output) []string {
	t.Helper()
	const prefix = "read: incomplete; next_call: mread "
	if output.ExitCode != 1 || !strings.HasPrefix(output.Stderr, prefix) {
		t.Fatalf("expected continuation: %+v", output)
	}
	return strings.Fields(strings.TrimPrefix(output.Stderr, prefix))
}

func TestRegisteredRecoveryRestartsAndReusesCursor(t *testing.T) {
	directory := t.TempDir()
	service, executable, selected, _ := composedRecovery(t, directory)
	text := strings.Repeat("retained α🙂 output\n", 100)
	id := publish(t, service, selected, command.OmittedOutput{Stdout: text})
	first := execute(t, executable, selected, id, "--max-tokens", "64")
	next := continuation(t, first)
	if first.Stdout == "" {
		t.Fatal("first page made no progress")
	}
	_, restarted, selected, _ := composedRecovery(t, directory)
	if replay := execute(t, restarted, selected, id, "--max-tokens", "64"); replay != first {
		t.Fatalf("repeated read changed page or cursor: first=%+v replay=%+v", first, replay)
	}
	joined := first.Stdout
	for range 100 {
		page := execute(t, restarted, selected, next...)
		joined += page.Stdout
		if page.ExitCode == 0 {
			if page.Stderr != "" || joined != text {
				t.Fatalf("recovered bytes=%q stderr=%q", joined, page.Stderr)
			}
			return
		}
		next = continuation(t, page)
	}
	t.Fatal("recovery did not finish")
}

func TestRegisteredRecoveryPreservesSuccessBesideInvalidReference(t *testing.T) {
	service, executable, selected, _ := composedRecovery(t, t.TempDir())
	id := publish(t, service, selected, command.OmittedOutput{Stdout: "saved stdout\n", Stderr: "saved stderr\n"})
	missing := history.ShortHandle(100)
	for _, args := range [][]string{{missing, id}, {id, missing}} {
		got := execute(t, executable, selected, args...)
		if got.ExitCode != 1 || !strings.Contains(got.Stdout, "saved stdout\n") || !strings.Contains(got.Stdout, "saved stderr\n") || !strings.Contains(got.Stderr, "mread: "+missing+": ") || strings.Contains(got.Stderr, "next_call:") {
			t.Fatalf("mixed read=%+v", got)
		}
	}
}

func TestRegisteredRecoveryBindsRowsAndStream(t *testing.T) {
	service, executable, selected, _ := composedRecovery(t, t.TempDir())
	text := strings.Repeat("retained row\n", 30)
	id := publish(t, service, selected, command.OmittedOutput{Stdout: text, StdoutKind: "rows", SourceRow: 41, Stderr: "captured diagnostic\n"})
	first := execute(t, executable, selected, id, "--stdout", "--max-tokens", "64")
	next := continuation(t, first)
	if !strings.Contains(first.Stdout, "[rows 41:") || strings.Contains(first.Stdout, "captured diagnostic") {
		t.Fatalf("row provenance=%+v", first)
	}
	inherited := execute(t, executable, selected, next...)
	if inherited.Stdout == "" || strings.Contains(inherited.Stdout, "captured diagnostic") {
		t.Fatalf("inherited stream=%+v", inherited)
	}
	changed := execute(t, executable, selected, append(next, "--stderr")...)
	if changed.ExitCode != 1 || changed.Stdout != "" || !strings.Contains(changed.Stderr, "continuation already binds a stream") || strings.Contains(changed.Stderr, "next_call:") {
		t.Fatalf("changed selection=%+v", changed)
	}
	stderr := execute(t, executable, selected, id, "--stderr")
	if stderr.ExitCode != 0 || !strings.Contains(stderr.Stdout, "[stderr bytes]\ncaptured diagnostic\n\n[/stderr]\n") {
		t.Fatalf("initial reference stream=%+v", stderr)
	}
}

func TestRegisteredRecoveryWithholdsPageWhenCursorPublicationFails(t *testing.T) {
	service, executable, selected, store := composedRecovery(t, t.TempDir())
	id := publish(t, service, selected, command.OmittedOutput{Stdout: strings.Repeat("retained output\n", 100)})
	allocation, found, err := store.ReadScope(selected.Namespace)
	if err != nil || !found {
		t.Fatalf("allocation=%+v found=%v err=%v", allocation, found, err)
	}
	// A nonregular managed record prevents cursor admission. No page or
	// continuation may be exposed until publication can succeed.
	next := history.ShortHandle(allocation.Next)
	name := fmt.Sprintf("output-%x.json", sha256.Sum256([]byte(selected.Namespace+"\x00"+next)))
	if err := os.Mkdir(filepath.Join(selected.Directory, name), 0700); err != nil {
		t.Fatal(err)
	}
	got := execute(t, executable, selected, id, "--max-tokens", "64")
	if got.ExitCode != 1 || got.Stdout != "" || got.Stderr == "" || strings.Contains(got.Stderr, "next_call:") {
		t.Fatalf("failed publication exposed a page or continuation: %+v", got)
	}
	if err := os.Remove(filepath.Join(selected.Directory, name)); err != nil {
		t.Fatal(err)
	}
	repaired := execute(t, executable, selected, id, "--max-tokens", "64")
	continuation(t, repaired)
	if repaired.Stdout == "" {
		t.Fatal("retry did not publish a page")
	}
}

func TestRegisteredRecoveryUsesContributedSelectionResolver(t *testing.T) {
	owner := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{RecordKinds: recovery.RecordKinds()}, nil)
	directory := t.TempDir()
	if _, err := owner.Open(t.Context(), directory); err != nil {
		t.Fatal(err)
	}
	selected := scope.Retained{Directory: directory, Namespace: "thread", Thread: "thread"}
	resolved, inspected := false, false
	resolver := reference.Resolver{
		Field: "changes",
		Canonicalize: func(data []byte, strict bool) ([]byte, error) {
			if !strict || strings.TrimSpace(string(data)) != `{"selection":1}` {
				return nil, fmt.Errorf("invalid selection: %s", data)
			}
			return []byte(`{"selection":1}`), nil
		},
		Resolve: func(ctx context.Context, view reference.View, data []byte) (command.OmittedOutput, error) {
			if ctx.Err() != nil || view.Scope != selected || string(data) != `{"selection":1}` {
				t.Fatalf("resolver input scope=%+v selection=%s", view.Scope, data)
			}
			resolved = true
			return command.OmittedOutput{Stdout: "retained selection\n"}, view.Retain()
		},
		Dependencies: func(got scope.Retained, data []byte) ([]string, error) {
			if got != selected || string(data) != `{"selection":1}` {
				t.Fatalf("dependency input scope=%+v selection=%s", got, data)
			}
			inspected = true
			return nil, nil
		},
	}
	service := moduletest.New(t, outputrecovery.Module, outputrecovery.Deps{History: owner}, module.Slots{References: []reference.Resolver{resolver}}, new(moduletest.Scripted))
	session, err := service.Attach(selected, recovery.DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := session.PublishSelection(t.Context(), []byte(` {"selection":1} `))
	if err != nil {
		t.Fatal(err)
	}
	executable := moduletest.BindCommand(outputrecovery.Module.Contributes.Commands[0], service)
	got := execute(t, executable, selected, id)
	if got.ExitCode != 0 || got.Stdout != "retained selection\n" || got.Stderr != "" || !resolved || !inspected {
		t.Fatalf("selection recovery=%+v resolved=%t inspected=%t", got, resolved, inspected)
	}
}

func TestRegisteredRecoveryRepairsInterruptedCursorPublication(t *testing.T) {
	service, executable, selected, _ := composedRecovery(t, t.TempDir())
	id := publish(t, service, selected, command.OmittedOutput{Stdout: strings.Repeat("retained output\n", 100)})
	first := execute(t, executable, selected, id, "--max-tokens", "64")
	next := continuation(t, first)[0]
	indexes, err := filepath.Glob(filepath.Join(selected.Directory, "cursor-*.json"))
	if err != nil || len(indexes) != 1 {
		t.Fatalf("cursor indexes: %v %v", indexes, err)
	}
	index, err := os.ReadFile(indexes[0])
	if err != nil || string(index) != fmt.Sprintf("%q", next) {
		t.Fatalf("committed cursor index: %s %v", index, err)
	}
	// Recreate interruption between index publication and cursor-record publication.
	name := fmt.Sprintf("output-%x.json", sha256.Sum256([]byte(selected.Namespace+"\x00"+next)))
	if err := os.Remove(filepath.Join(selected.Directory, name)); err != nil {
		t.Fatal(err)
	}
	_, restarted, scope, _ := composedRecovery(t, selected.Directory)
	repaired := execute(t, restarted, scope, id, "--max-tokens", "64")
	if got := continuation(t, repaired)[0]; got != next || repaired.Stdout != first.Stdout {
		t.Fatalf("repair changed page or identity: %+v", repaired)
	}
	if _, err := os.Stat(filepath.Join(selected.Directory, name)); err != nil {
		t.Fatalf("cursor record not repaired: %v", err)
	}
	page := execute(t, restarted, scope, next, "--max-tokens", "64")
	if page.Stdout == "" || !strings.Contains(page.Stdout, "retained output") {
		t.Fatalf("repaired continuation unusable: %+v", page)
	}
}
