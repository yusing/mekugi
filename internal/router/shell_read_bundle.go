package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/shellruntime"
	"github.com/yusing/mekugi/internal/verifiedrow"
	"mvdan.cc/sh/v3/interp"
)

const readBundleUsage = "hcat --batch [--max-tokens N] PATH [START:END] -- PATH [START:END] ..."

type readBundleSpec struct {
	path string
	span string
}

func parseReadBundle(args []string) ([]readBundleSpec, int, error) {
	budget := 4000
	if len(args) > 0 && args[0] == "--max-tokens" {
		if len(args) < 2 {
			return nil, 0, errors.New(readBundleUsage)
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 || n > hrunMaxTokens || strconv.Itoa(n) != args[1] {
			return nil, 0, errors.New("--max-tokens must be an integer from 1 through 15500")
		}
		budget, args = n, args[2:]
	}
	var specs []readBundleSpec
	for len(args) > 0 {
		end := 0
		for end < len(args) && args[end] != "--" {
			end++
		}
		if end < 1 || end > 2 || args[0] == "" || len(specs) == 16 {
			return nil, 0, errors.New(readBundleUsage + " (1–16 files)")
		}
		spec := readBundleSpec{path: args[0]}
		if end == 2 {
			spec.span = args[1]
		}
		specs = append(specs, spec)
		if end == len(args) {
			break
		}
		args = args[end+1:]
		if len(args) == 0 {
			return nil, 0, errors.New(readBundleUsage)
		}
	}
	if len(specs) == 0 {
		return nil, 0, errors.New(readBundleUsage)
	}
	return specs, budget, nil
}

// Bundle composition delegates source parsing, verified rows and row admission to
// hcat. Only framing and allocation belong here; omitted rows use the hread store.
func executeReadBundle(ctx context.Context, manifest toolWorkerManifest, runtime string, args []string, hcat toolContribution, shellID string) error {
	handler := interp.HandlerCtx(ctx)
	fail := func(err error) error {
		_, _ = fmt.Fprintf(handler.Stderr, "hcat --batch: %v\n", err)
		return interp.ExitStatus(1)
	}
	specs, budget, err := parseReadBundle(args)
	if err != nil {
		return fail(err)
	}
	reserve := 0
	for _, spec := range specs {
		// UTF-8/JSON escaped byte lengths conservatively bound framing tokens.
		// Keep the manifest ahead of bodies so every file has a visible receipt.
		reserve += len(mustMarshalJSON(spec.path))*2 + 512
	}
	if budget-reserve < len(specs) {
		return fail(errors.New("budget cannot fit the bundle manifest; increase --max-tokens or select fewer paths"))
	}
	share := (budget - reserve) / len(specs)
	store, err := shellOutputStore(manifest)
	if err != nil {
		return fail(err)
	}
	var manifestText, bodies strings.Builder
	incomplete := false
	for i, spec := range specs {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		execution, err := readBundleFile(ctx, manifest, runtime, spec, share, hcat, shellID)
		if err != nil {
			return fail(err)
		}
		state := "complete"
		omitted, next := "none", ""
		if execution.ExitCode != 0 {
			incomplete = true
			state = "failed"
			if execution.FailureClass == "output_limit" {
				state, omitted = "incomplete", "unavailable"
			}
		}
		remainder := toolplugin.OmittedOutput{Stderr: execution.Stderr}
		if execution.OmittedOutput != nil {
			remainder.Stdout = execution.OmittedOutput.Stdout
			remainder.StdoutKind = execution.OmittedOutput.StdoutKind
			remainder.Stderr += execution.OmittedOutput.Stderr
			omitted = bundleRowSpan(remainder.Stdout)
		}
		if remainder.Stdout != "" || remainder.Stderr != "" {
			id, err := store.putTypedOutput(ctx, remainder, execution.ExitCode)
			if err != nil {
				return fail(err)
			}
			next = "hread " + id
		}
		fmt.Fprintf(&manifestText, "%d path=%s shown=%s omitted=%s status=%s", i+1,
			mustMarshalJSON(spec.path), bundleRowSpan(execution.Stdout), omitted, state)
		if next != "" {
			fmt.Fprintf(&manifestText, " next_call=%q", next)
		}
		manifestText.WriteByte('\n')
		if execution.Stdout != "" {
			fmt.Fprintf(&bodies, "\n--- file %d ---\n%s", i+1, execution.Stdout)
		}
	}
	if _, err := io.WriteString(handler.Stdout, manifestText.String()+bodies.String()); err != nil {
		return err
	}
	if incomplete {
		return interp.ExitStatus(1)
	}
	return nil
}

func bundleRowSpan(text string) string {
	first, last := uint64(0), uint64(0)
	for line := range strings.SplitSeq(text, "\n") {
		ref, _, _ := strings.Cut(line, " ")
		row, err := verifiedrow.ParseReference(ref)
		if err != nil {
			continue
		}
		if first == 0 {
			first = row.Line
		}
		last = row.Line
	}
	if first == 0 {
		return "none"
	}
	return fmt.Sprintf("%d:%d", first, last)
}

func readBundleFile(ctx context.Context, manifest toolWorkerManifest, runtime string, spec readBundleSpec, tokens int, hcat toolContribution, shellID string) (execution toolplugin.ExecutionOutput, err error) {
	handler := interp.HandlerCtx(ctx)
	journal := manifest.AXReadOutput
	if journal == "" {
		journal = handler.Env.Get(capturer.AXReadOutputEnvironment).String()
	}
	invocation, _ := ctx.Value(shellInvocationContextKey{}).(shellInvocation)
	observation, observeErr := capturer.StartAXReadWithContext(journal,
		handler.Env.Get(shellruntime.ThreadIDEnvironment).String(), "hcat",
		capturer.AXReadContext{CallID: invocation.CallID, ShellID: shellID})
	if observeErr != nil {
		_, _ = fmt.Fprintf(handler.Stderr, "shell: AX read evidence unavailable: %v\n", observeErr)
	}
	defer func() {
		class := execution.FailureClass
		if err != nil {
			class = "execution_error"
		}
		if ctx.Err() != nil {
			class = "canceled"
		}
		var exitCode *int
		if err == nil {
			exitCode = new(execution.ExitCode)
		}
		if e := observation.FinishResult(err == nil && execution.ExitCode == 0, class, exitCode); e != nil {
			_, _ = fmt.Fprintf(handler.Stderr, "shell: AX read evidence incomplete: %v\n", e)
		}
	}()
	path := spec.path
	var input *os.File
	if strings.HasPrefix(path, shellArtifactPrefix) {
		directory := handler.Env.Get(shellruntime.RuntimeDirectoryEnvironment).String()
		if directory == "" {
			directory = os.TempDir()
		}
		input, err = openRetainedShellFile(directory, handler.Env.Get(shellruntime.ThreadIDEnvironment).String(), path)
		if err != nil {
			return toolplugin.ExecutionOutput{Stderr: fmt.Sprintf("hcat: %v\n", err), ExitCode: 1, FailureClass: "retained_file"}, nil
		}
		defer input.Close()
		path = "/dev/fd/3"
	}
	args := []string{"--max-tokens", strconv.Itoa(tokens), "--", path}
	if spec.span != "" {
		args = append(args, spec.span)
	}
	return toolplugin.Execute(ctx, manifest.NodeExecutable, runtime, hcat.Module, hcat.ModuleIndex,
		args, input, handler.Dir, shellEnvironment(handler.Env))
}
