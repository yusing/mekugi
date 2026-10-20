package fileread_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/fileread"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestRegisteredReader(t *testing.T) {
	script := new(moduletest.Scripted)
	service := moduletest.New(t, fileread.Module, fileread.Deps{}, module.Slots{}, script)
	contributions, registered := fileread.Module.Contributes.Commands, script.Commands()
	if fileread.Module.Lifetime != module.Stateless || len(contributions) != 1 || len(registered) != 1 {
		t.Fatalf("lifetime=%v contributions=%v registered=%v", fileread.Module.Lifetime, contributions, registered)
	}
	reader := moduletest.BindCommand(contributions[0], service)
	if reader.Name != "mcat" || reader.Run == nil || !reader.ObserveRead || reader.Spec != registered[0] {
		t.Fatalf("registered reader=%+v", reader)
	}
	if err := reader.Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("first\r\nsecond\rthird"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Run(t.Context(), command.Request{Args: []string{path, "2:3"}, Stdin: nil})
	if err != nil || got.Stdout != "second\nthird\n" || got.Stderr != "" || got.ExitCode != 0 || got.Delivery != nil || got.OmittedOutput != nil {
		t.Fatalf("registered read=%+v err=%v", got, err)
	}
	for _, args := range [][]string{{path, "1:9007199254740992"}, {"-n", "9007199254740992", path}} {
		got, err := reader.Run(t.Context(), command.Request{Args: args, Stdin: nil})
		if err != nil || got.FailureClass != "invalid_arguments" || got.ExitCode == 0 || got.Stdout != "" {
			t.Fatalf("unsafe integer %q: output=%+v err=%v", args, got, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err = reader.Run(ctx, command.Request{Args: []string{path}, Stdin: nil})
	if !errors.Is(err, context.Canceled) || got != (command.Output{}) {
		t.Fatalf("canceled read=%+v err=%v", got, err)
	}
}
