package symbolquery_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/symbolquery"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func registeredCommand(t *testing.T) command.Contribution {
	t.Helper()
	script := new(moduletest.Scripted)
	service := moduletest.New(t, symbolquery.Module, symbolquery.Deps{}, module.Slots{}, script)
	commands, registered := symbolquery.Module.Contributes.Commands, script.Commands()
	if symbolquery.Module.Lifetime != module.HostOperation || len(commands) != 1 || len(registered) != 1 {
		t.Fatalf("lifetime=%v commands=%v registered=%v", symbolquery.Module.Lifetime, commands, registered)
	}
	got := moduletest.BindCommand(commands[0], service)
	if got.Name != "msymbol" || got.Run == nil || !got.ObserveRead || registered[0] != got.Spec {
		t.Fatalf("registered command=%+v", got)
	}
	if err := got.Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRegisteredSymbolRejectsInvalidInputsWithoutResolver(t *testing.T) {
	t.Setenv("PATH", "")
	contribution := registeredCommand(t)
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("# Item\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, stderr, class string
		args                []string
	}{
		{"mode", "msymbol: mode must be def or refs\n", "invalid_arguments", []string{"lookup", "notes.md", "Item"}},
		{"source", "msymbol: path has an unsupported msymbol source format\n", "invalid_source", []string{"--workspace", workspace, "def", "notes.md", "Item"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := contribution.Run(t.Context(), command.Request{Args: tc.args, Stdin: nil})
			if err != nil || got.Stdout != "" || got.Stderr != tc.stderr || got.ExitCode != 1 || got.FailureClass != tc.class || got.OmittedOutput != nil {
				t.Fatalf("output=%+v err=%v", got, err)
			}
		})
	}
}

func TestRegisteredSymbolHonorsCancellation(t *testing.T) {
	t.Parallel()
	contribution := registeredCommand(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := contribution.Run(ctx, command.Request{Args: []string{"def", "missing.go", "Item"}, Stdin: nil})
	if !errors.Is(err, context.Canceled) || got != (command.Output{}) {
		t.Fatalf("canceled output=%+v err=%v", got, err)
	}
}
