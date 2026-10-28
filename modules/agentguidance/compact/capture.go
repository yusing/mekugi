package compact

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os/exec"
	"sync"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"
)

const SnapshotPrefix = "Reload snapshot: mread "

// CaptureCommand uses the authenticated worker's existing retained-output
// delivery. Source bytes never pass through stock tool output windows.
func CaptureCommand(selection string) string { return "mreload " + commandargs.Quote(selection) }

type Snapshot struct {
	Selection string
	Sources   []CapturedSource
}

type CapturedSource struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
}

func Capture(ctx context.Context, request command.Request) (command.Output, error) {
	if len(request.Args) != 1 {
		return command.Output{}, errors.New("mreload requires one reload selection")
	}
	snapshot, err := ReadSources(ctx, request.Args[0], "", nil)
	if err != nil {
		return command.Output{}, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return command.Output{}, err
	}
	return command.Output{Delivery: capturedDelivery{string(data)}}, nil
}

// ReadSources acquires the shared selection in an admitted runtime environment.
// The caller owns policy admission and durable context retention.
func ReadSources(ctx context.Context, selection, directory string, environment []string) (Snapshot, error) {
	sources, err := Sources(selection)
	if err != nil {
		return Snapshot{}, err
	}
	commands, err := ReadCommands(selection)
	if err != nil {
		return Snapshot{}, err
	}
	// Keep original selection indices and command identity for retained history.
	// Host-loaded instructions have no acquisition or result body.
	results := make([]CapturedSource, len(sources))
	var readers sync.WaitGroup
	commandIndex := 0
	for i, source := range sources {
		if source.HostLoaded() {
			continue
		}
		script := commands[commandIndex]
		commandIndex++
		readers.Go(func() {
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(ctx, "bash", "-c", script)
			cmd.Dir, cmd.Env = directory, environment
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			code := 0
			if err := cmd.Run(); err != nil {
				code = 1
				if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() > 0 {
					code = exit.ExitCode()
				}
				if stderr.Len() == 0 {
					fmt.Fprintln(&stderr, err)
				}
			}
			results[i] = CapturedSource{script, stdout.String(), stderr.String(), code}
		})
	}
	readers.Wait()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Selection: selection, Sources: results}, nil
}

type capturedDelivery struct{ data string }

func (capturedDelivery) ReferenceCount() int { return 1 }

func (d capturedDelivery) Prepare(refs []string) (command.PreparedDelivery, error) {
	if len(refs) != 1 {
		return command.PreparedDelivery{}, errors.New("reload snapshot requires one reference")
	}
	return command.PreparedDelivery{
		Referenced:   command.Output{Stdout: SnapshotPrefix + refs[0] + "\n"},
		Unreferenced: command.Output{Stderr: "CONTENT NOT ATTACHED: reload snapshot publication failed\n", ExitCode: 1},
		Recovery:     []command.RecoveryOutput{{OmittedOutput: command.OmittedOutput{Stdout: d.data}}},
	}, nil
}
