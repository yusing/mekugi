package sourceoutline_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sourceoutline"
	"github.com/yusing/mekugi/modules/sourceoutline/outline"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func registeredCommand(t *testing.T) command.Contribution {
	t.Helper()
	script := new(moduletest.Scripted)
	service := moduletest.New(t, sourceoutline.Module, sourceoutline.Deps{}, module.Slots{}, script)
	commands, registered := sourceoutline.Module.Contributes.Commands, script.Commands()
	if sourceoutline.Module.Lifetime != module.Stateless || len(commands) != 1 || len(registered) != 1 {
		t.Fatalf("lifetime=%v commands=%v registered=%v", sourceoutline.Module.Lifetime, commands, registered)
	}
	got := moduletest.BindCommand(commands[0], service)
	if got.Name != "inspect_file" || got.Run == nil || !got.ObserveRead || registered[0] != got.Spec {
		t.Fatalf("registered command=%+v", got)
	}
	if err := got.Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	return got
}

func sourceFixture(t *testing.T, name, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRegisteredOutlineMatchesSnapshotWithoutRuntime(t *testing.T) {
	// Extraction uses the portable parser even when no external runtime is on PATH.
	t.Setenv("PATH", "")
	contribution := registeredCommand(t)
	source := "package p\n\nimport \"fmt\"\nimport \"strings\"\n\nfunc First() {}\nfunc Second() {}\n"
	path := sourceFixture(t, "sample.go", source)
	got, err := contribution.Run(t.Context(), command.Request{Args: []string{path}, Stdin: nil})
	if err != nil {
		t.Fatal(err)
	}
	want := "3-4 import\n6-6 function First\n7-7 function Second\n"
	if got.Stdout != want || got.Stderr != "" || got.ExitCode != 0 || got.OmittedOutput != nil {
		t.Fatalf("output=%+v, want stdout=%q", got, want)
	}
	// The snapshot API must consume its supplied bytes, independently of the file.
	if err := os.WriteFile(path, []byte("package replaced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (outline.Service{}).InspectSource(t.Context(), path, []byte(source))
	if err != nil || snapshot != got.Stdout {
		t.Fatalf("snapshot=%q err=%v, want %q", snapshot, err, got.Stdout)
	}
}

func TestRegisteredOutlineHonorsCancellation(t *testing.T) {
	t.Parallel()
	contribution := registeredCommand(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := contribution.Run(ctx, command.Request{Args: []string{filepath.Join(t.TempDir(), "missing.go")}, Stdin: nil})
	if !errors.Is(err, context.Canceled) || got != (command.Output{}) {
		t.Fatalf("canceled output=%+v err=%v", got, err)
	}
}
