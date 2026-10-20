package boundedcommand_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/boundedcommand"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func registeredCommand(t *testing.T) command.Contribution {
	t.Helper()
	script := new(moduletest.Scripted)
	service := moduletest.New(t, boundedcommand.Module, boundedcommand.Deps{}, module.Slots{}, script)
	commands := boundedcommand.Module.Contributes.Commands
	registered := script.Commands()
	if boundedcommand.Module.Lifetime != module.HostOperation || len(commands) != 1 {
		t.Fatalf("lifetime=%v commands=%v", boundedcommand.Module.Lifetime, commands)
	}
	got := moduletest.BindCommand(commands[0], service)
	if got.Name != "mrun" || got.Run == nil || len(registered) != 1 || registered[0] != got.Spec {
		t.Fatalf("registered command=%+v", got)
	}
	if err := got.Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	return got
}

func fixtureArgv(t *testing.T, mode string, arguments ...string) []string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{executable, "-test.run=^TestBoundedCommandProcess$", "--", "--boundedcommand-fixture", mode}, arguments...)
}

func TestRegisteredCommandPreservesArgvStdinAndStatus(t *testing.T) {
	t.Parallel()
	contribution := registeredCommand(t)
	argv := fixtureArgv(t, "echo", "", "two words", "$literal;*", "--tail", "日本語")
	input := "stdin\x00with\r\nbytes\n"
	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close() })
	if _, err := stdin.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := contribution.Run(t.Context(), command.Request{Args: append([]string{"-n", "10", "--"}, argv...), Stdin: stdin})
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%q\n", argv); got.Stdout != want || got.Stderr != input || got.ExitCode != 23 || got.OmittedOutput != nil {
		t.Fatalf("output=%+v, want stdout=%q stderr=%q exit=23", got, want, input)
	}
}

func TestRegisteredCommandSelectsAndDiscardsRows(t *testing.T) {
	t.Parallel()
	contribution := registeredCommand(t)
	for _, tail := range []bool{false, true} {
		t.Run(fmt.Sprintf("tail=%t", tail), func(t *testing.T) {
			arguments := []string{"-n", "2"}
			want := "row 00000\nrow 00001\n"
			if tail {
				arguments = append(arguments, "--tail")
				want = "row 00003\nrow 00004\n"
			}
			arguments = append(arguments, "--")
			arguments = append(arguments, fixtureArgv(t, "rows", "5")...)
			got, err := contribution.Run(t.Context(), command.Request{Args: arguments, Stdin: nil})
			if err != nil {
				t.Fatal(err)
			}
			if got.Stdout != want || got.Stderr != "mrun: output incomplete: 2-line limit reached\n" || got.ExitCode != 0 || got.OmittedOutput != nil {
				t.Fatalf("output=%+v, want stdout=%q and no retained discarded rows", got, want)
			}
		})
	}
}

func TestRegisteredCommandRetainsOnlySelectedDeliveryOverflow(t *testing.T) {
	t.Parallel()
	contribution := registeredCommand(t)
	arguments := append([]string{"-n", "8000", "--tail", "--"}, fixtureArgv(t, "rows", "10000")...)
	got, err := contribution.Run(t.Context(), command.Request{Args: arguments, Stdin: nil})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 0 || got.OmittedOutput == nil || got.Stdout == "" || got.OmittedOutput.Stdout == "" {
		t.Fatalf("exit=%d delivered bytes=%d omitted=%+v", got.ExitCode, len(got.Stdout), got.OmittedOutput)
	}
	var selected strings.Builder
	for i := 2000; i < 10000; i++ {
		fmt.Fprintf(&selected, "row %05d\n", i)
	}
	if got.Stdout+got.OmittedOutput.Stdout != selected.String() {
		t.Fatal("delivered and retained output do not reconstruct exactly the selected tail")
	}
	if got.Stderr != "mrun: output incomplete: 8000-line limit reached\n" || got.OmittedOutput.Stderr != "" || got.OmittedOutput.StdoutKind != "rows" {
		t.Fatalf("stderr=%q omitted stderr=%q kind=%q", got.Stderr, got.OmittedOutput.Stderr, got.OmittedOutput.StdoutKind)
	}
}

func TestRegisteredCommandCancelsRunningChild(t *testing.T) {
	t.Parallel()
	contribution := registeredCommand(t)
	ready := filepath.Join(t.TempDir(), "ready")
	stdin, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	arguments := append([]string{"-n", "1", "--"}, fixtureArgv(t, "wait", ready)...)
	go func() {
		_, err := contribution.Run(ctx, command.Request{Args: arguments, Stdin: stdin})
		done <- err
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			t.Fatalf("child exited before readiness: %v", err)
		case <-deadline.C:
			t.Fatal("child did not become ready")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("running child did not stop after cancellation")
	}
}

// The test binary is the child fixture, avoiding shell parsing and external tools.
func TestBoundedCommandProcess(t *testing.T) {
	index := slices.Index(os.Args, "--boundedcommand-fixture")
	if index < 0 {
		return
	}
	arguments := os.Args[index+1:]
	switch arguments[0] {
	case "echo":
		fmt.Fprintf(os.Stdout, "%q\n", os.Args)
		if _, err := io.Copy(os.Stderr, os.Stdin); err != nil {
			os.Exit(24)
		}
		os.Exit(23)
	case "rows":
		var count int
		if _, err := fmt.Sscan(arguments[1], &count); err != nil {
			os.Exit(24)
		}
		for i := range count {
			fmt.Fprintf(os.Stdout, "row %05d\n", i)
		}
	case "wait":
		if err := os.WriteFile(arguments[1], nil, 0600); err != nil {
			os.Exit(24)
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			os.Exit(24)
		}
	default:
		os.Exit(24)
	}
	os.Exit(0)
}
