package router

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

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

// RunToolPluginWorker handles the private child-process mode used by a
// stable contributed-tool frontend or the current thread's shell runtime.
func RunToolPluginWorker(
	ctx context.Context,
	argv0 string,
	args []string,
	stdin *os.File,
	stdout, stderr io.Writer,
) (bool, int) {
	invokedName := filepath.Base(argv0)
	candidate := argv0
	if invokedName == candidate {
		var err error
		candidate, err = exec.LookPath(candidate)
		if err != nil {
			return false, 0
		}
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return false, 0
	}
	info, err := os.Lstat(candidate)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false, 0
	}
	fail := func(err error) (bool, int) {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", invokedName, err)
		return true, 1
	}

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
	_, snapshotWrapper := toolRegistryIDFromDirectory(directory)
	if !snapshotWrapper {
		if filepath.Base(directory) != "bin" {
			return false, 0
		}
		if _, authenticated := toolRegistryIDFromDirectory(filepath.Dir(directory)); !authenticated {
			return false, 0
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
		_, snapshotWrapper = toolRegistryIDFromDirectory(directory)
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

	return runAuthenticatedToolWorker(ctx, directory, filepath.Base(wrapper), args, stdin, stdout, stderr)
}

func runAuthenticatedToolWorker(
	ctx context.Context,
	directory, name string,
	args []string,
	stdin *os.File,
	stdout, stderr io.Writer,
) (bool, int) {
	fail := func(err error) (bool, int) {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return true, 1
	}
	expectedRegistryID, authenticated := toolRegistryIDFromDirectory(directory)
	if !authenticated {
		return fail(errors.New("tool worker snapshot is not authenticated"))
	}
	manifest, err := readToolWorkerManifest(filepath.Join(directory, toolPluginManifestFilename))
	if err != nil {
		return fail(err)
	}
	if manifest.Version != 1 || manifest.RuntimeRoot != "runtime" || manifest.NodeExecutable == "" {
		return fail(errors.New("tool worker manifest is inconsistent"))
	}
	if manifest.RegistryID != expectedRegistryID {
		return fail(errors.New("tool worker registry identity mismatch"))
	}
	runtimeRoot := filepath.Join(directory, manifest.RuntimeRoot)
	identity, err := toolRegistryIdentity(manifest, runtimeRoot)
	if err != nil {
		return fail(err)
	}
	if identity != expectedRegistryID {
		return fail(errors.New("tool worker registry identity mismatch"))
	}

	var contribution *toolContribution
	for index := range manifest.Tools {
		if manifest.Tools[index].Name == name {
			if contribution != nil {
				return fail(fmt.Errorf("tool %q appears more than once in worker manifest", name))
			}
			contribution = &manifest.Tools[index]
		}
	}
	if contribution == nil || !contribution.Executable || contribution.PluginID == "" {
		return fail(fmt.Errorf("tool %q is unavailable in worker manifest", name))
	}
	if !contribution.Builtin {
		if contribution.Module == "" {
			return fail(fmt.Errorf("tool %q has no executable module", name))
		}
		if err := validateToolContribution(*contribution); err != nil {
			return fail(err)
		}
	}
	if manifest.ReplayDirectory != "" {
		store, err := shellOutputStore(manifest)
		if err != nil {
			return fail(err)
		}
		store.storageNotice = func(_ string, message string) { _, _ = fmt.Fprintln(stderr, message) }
		var release func()
		ctx, release, err = store.beginSession(ctx, os.Getenv("CODEX_THREAD_ID"), "")
		if err != nil {
			return fail(err)
		}
		defer release()
	}
	if !contribution.Builtin && contribution.PluginID == builtinToolsPluginID && contribution.Name == "shell" {
		if handled, publishErr := publishCommentaryOnce(ctx, stdout, args); handled {
			if publishErr != nil {
				return fail(publishErr)
			}
			return true, 0
		}
	}

	var execution toolplugin.ExecutionOutput
	if contribution.Builtin {
		switch contribution.Name {
		case "mread":
			execution = executeMRead(ctx, manifest, runtimeRoot, args)
		case "mchanges":
			execution = executeMChanges(ctx, manifest, runtimeRoot, args)
		case "mrun":
			execution, err = executeMRun(ctx, manifest, runtimeRoot, args, stdin)
		default:
			return fail(fmt.Errorf("built-in tool %q is unavailable", name))
		}
	} else if contribution.PluginID == builtinToolsPluginID && contribution.Name == "mcat" {
		execution, err = executeMCat(ctx, manifest, runtimeRoot, args, *contribution)
	} else if contribution.PluginID == builtinToolsPluginID && (contribution.Name == "msymbol" || contribution.Name == "inspect_file") {
		execution, err = executeFrontendReader(ctx, manifest, runtimeRoot, args, *contribution)
	} else if contribution.PluginID == builtinToolsPluginID && contribution.Name == "shell" {
		workingDirectory, workingDirectoryErr := os.Getwd()
		if workingDirectoryErr != nil {
			err = fmt.Errorf("resolve shell working directory: %w", workingDirectoryErr)
		} else {
			execution, err = executeShellTool(ctx, manifest, runtimeRoot, contribution, args, stdin,
				workingDirectory, os.Environ(), discoverShellCommentary(filepath.Join(directory, name)), stdout, stderr)
		}
	} else {
		execution, err = toolplugin.Execute(
			ctx,
			manifest.NodeExecutable,
			runtimeRoot,
			contribution.Module,
			contribution.ModuleIndex,
			args,
			stdin,
			"",
			nil,
		)
	}
	if err != nil {
		return fail(fmt.Errorf("execute tool: %w", err))
	}
	execution, err = retainExecutionOutput(ctx, manifest, execution)
	if err != nil {
		return fail(fmt.Errorf("retain plugin output: %w", err))
	}
	if _, err := io.WriteString(stdout, execution.Stdout); err != nil {
		return fail(fmt.Errorf("write tool stdout: %w", err))
	}
	if _, err := io.WriteString(stderr, execution.Stderr); err != nil {
		return fail(fmt.Errorf("write tool stderr: %w", err))
	}
	return true, execution.ExitCode
}

func toolRegistryIDFromDirectory(directory string) (string, bool) {
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

func readToolWorkerManifest(path string) (manifest toolWorkerManifest, err error) {
	file, err := os.Open(path)
	if err != nil {
		return toolWorkerManifest{}, fmt.Errorf("open tool worker manifest: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	content, err := io.ReadAll(file)
	if err != nil {
		return toolWorkerManifest{}, fmt.Errorf("read tool worker manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return toolWorkerManifest{}, fmt.Errorf("decode tool worker manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return toolWorkerManifest{}, errors.New("tool worker manifest contains trailing data")
	}
	return manifest, nil
}
