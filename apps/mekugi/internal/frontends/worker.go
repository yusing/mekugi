// Source: apps/mekugi/internal/router/tool_plugin_worker.go:1:273@[7b6d9ccc]
package frontends

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/modules/outputrecovery/recovery"

	"github.com/yusing/mekugi/contracts/command"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Invocation identifies a pinned frontend executable, before its selected
// command is authenticated against the composed catalog.
type Invocation struct{ directory, name string }

// Recognize distinguishes private frontend invocations from ordinary
// launches. A nil invocation and nil error means the executable is not a worker.
func Recognize(argv0 string) (*Invocation, error) {
	invokedName := filepath.Base(argv0)
	candidate := argv0
	if invokedName == candidate {
		var err error
		candidate, err = exec.LookPath(candidate)
		if err != nil {
			return nil, nil
		}
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return nil, nil
	}
	info, err := os.Lstat(candidate)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return nil, nil
	}
	fail := func(err error) (*Invocation, error) { return nil, fmt.Errorf("%s: %w", invokedName, err) }

	executable, err := openRunningExecutable()
	if err != nil {
		return fail(fmt.Errorf("locate mekugi executable: %w", err))
	}
	defer executable.Close()
	executableInfo, err := executable.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect mekugi executable: %w", err))
	}

	wrapper := candidate
	directory := filepath.Dir(wrapper)
	_, snapshotWrapper := RegistryIDFromDirectory(directory)
	if !snapshotWrapper {
		if filepath.Base(directory) != "bin" {
			return nil, nil
		}
		if _, authenticated := RegistryIDFromDirectory(filepath.Dir(directory)); !authenticated {
			return nil, nil
		}
		target, readErr := os.Readlink(candidate)
		if readErr != nil {
			return fail(fmt.Errorf("resolve tool frontend: %w", readErr))
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(candidate), target)
		}
		wrapper = filepath.Clean(target)
		if filepath.Dir(wrapper) != filepath.Dir(directory) || filepath.Base(wrapper) != invokedName {
			return fail(errors.New("tool frontend and snapshot wrapper names differ"))
		}
		wrapperInfo, wrapperErr := os.Lstat(wrapper)
		directory = filepath.Dir(wrapper)
		_, snapshotWrapper = RegistryIDFromDirectory(directory)
		if wrapperErr != nil || wrapperInfo.Mode()&os.ModeSymlink == 0 || !snapshotWrapper {
			return fail(errors.New("tool frontend does not target an authenticated snapshot wrapper"))
		}
	}
	if filepath.Base(wrapper) != invokedName {
		return fail(errors.New("invoked tool and snapshot wrapper names differ"))
	}

	target, err := os.Stat(wrapper)
	if err != nil {
		return fail(fmt.Errorf("resolve tool wrapper: %w", err))
	}
	if !os.SameFile(target, executableInfo) {
		return fail(errors.New("tool wrapper does not target the running mekugi executable"))
	}

	return &Invocation{directory: directory, name: filepath.Base(wrapper)}, nil
}

// Run authenticates and dispatches the selected command under the caller's
// process ownership. Only a dedicated host worker may claim DedicatedHostProcess.
func (invocation *Invocation) Run(service *history.Service, recovered *recovery.Service, agentMetrics *evidence.Service, commands []command.Contribution, processScope command.ProcessScope,
	ctx context.Context, args []string, stdin *os.File, stdout, stderr io.Writer,
) int {
	directory, name := invocation.directory, invocation.name
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	expectedRegistryID, authenticated := RegistryIDFromDirectory(directory)
	if !authenticated {
		return fail(errors.New("tool worker snapshot is not authenticated"))
	}
	manifest, err := readToolWorkerManifest(filepath.Join(directory, manifestFilename))
	if err != nil {
		return fail(err)
	}
	if manifest.Version != 2 {
		return fail(errors.New("tool worker manifest is inconsistent"))
	}
	if manifest.RegistryID != expectedRegistryID {
		return fail(errors.New("tool worker registry identity mismatch"))
	}
	identity, err := registryIdentity(manifest)
	if err != nil {
		return fail(err)
	}
	if identity != expectedRegistryID {
		return fail(errors.New("tool worker registry identity mismatch"))
	}

	var contribution *catalogEntry
	for index := range manifest.Tools {
		if manifest.Tools[index].Name == name {
			if contribution != nil {
				return fail(fmt.Errorf("tool %q appears more than once in worker manifest", name))
			}
			contribution = &manifest.Tools[index]
		}
	}
	if contribution == nil || !contribution.Executable {
		return fail(fmt.Errorf("tool %q is unavailable in worker manifest", name))
	}
	if err := validateToolContribution(*contribution); err != nil {
		return fail(err)
	}

	selected, release, err := beginSession(ctx, service, manifest.ReplayDirectory, os.Getenv("CODEX_THREAD_ID"))
	if err != nil {
		return fail(err)
	}
	defer release()
	execute, observeRead := frontendExecutor(*contribution, commands)
	if execute == nil {
		return fail(fmt.Errorf("native executor %q is unavailable", contribution.NativeExecutor))
	}
	run := func(ctx context.Context, request command.Request) (command.Output, error) {
		execution, err := execute(ctx, request)
		if err != nil {
			return execution, err
		}
		plan := execution.Delivery
		execution, err = retainExecutionOutput(ctx, recovered, selected, execution)
		if ctx.Err() != nil {
			return command.Output{}, ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return command.Output{}, err
		}
		if err != nil {
			if fallback, ok := plan.(command.RecoveryFailure); ok {
				return fallback.RecoveryFailed(err), nil
			}
			if execution.Stderr != "" && !strings.HasSuffix(execution.Stderr, "\n") {
				execution.Stderr += "\n"
			}
			execution.Stderr += fmt.Sprintf("%s: recovery unavailable: %v\n", name, err)
			if execution.ExitCode == 0 {
				execution.ExitCode = 1
			}
		}
		return execution, nil
	}
	request := command.Request{Args: args, Stdin: stdin, ProcessScope: processScope,
		Scope: selected}
	var execution command.Output
	if observeRead && agentMetrics != nil {
		execution, err = executeFrontendReader(ctx, agentMetrics, manifest.AXReadOutput, name, request, run)
	} else {
		execution, err = run(ctx, request)
	}
	if err != nil {
		return fail(fmt.Errorf("execute tool: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if _, err := io.WriteString(stdout, execution.Stdout); err != nil {
		return fail(fmt.Errorf("write tool stdout: %w", err))
	}
	if _, err := io.WriteString(stderr, execution.Stderr); err != nil {
		return fail(fmt.Errorf("write tool stderr: %w", err))
	}
	return execution.ExitCode
}

// frontendExecutor is the retained dispatch owner used both before catalog
// publication and when an authenticated worker executes a selected command.
func frontendExecutor(contribution catalogEntry, commands []command.Contribution) (func(context.Context, command.Request) (command.Output, error), bool) {
	for _, compiled := range commands {
		if compiled.Name == contribution.NativeExecutor && compiled.Run != nil {
			return compiled.Run, compiled.ObserveRead
		}
	}
	return nil, false
}

// RegistryIDFromDirectory recognizes the snapshot naming format. Manifest and
// executable authentication remain mandatory before dispatch.
func RegistryIDFromDirectory(directory string) (string, bool) {
	base := filepath.Base(directory)
	if !strings.HasPrefix(base, "mekugi-tools-") {
		return "", false
	}
	_, registryID, found := strings.CutLast(base, "-")
	if !found || registryID == "" {
		return "", false
	}
	decoded, err := hex.DecodeString(registryID)
	return registryID, err == nil && len(decoded) == 32
}

func readToolWorkerManifest(path string) (manifest workerManifest, err error) {
	file, err := os.Open(path)
	if err != nil {
		return workerManifest{}, fmt.Errorf("open tool worker manifest: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	content, err := io.ReadAll(file)
	if err != nil {
		return workerManifest{}, fmt.Errorf("read tool worker manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return workerManifest{}, fmt.Errorf("decode tool worker manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return workerManifest{}, errors.New("tool worker manifest contains trailing data")
	}
	return manifest, nil
}
