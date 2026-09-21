package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/shellruntime"
)

const readBundleUsage = "mcat [--max-tokens N] PATH [START:END] [PATH [START:END] ...]"

type readBundleSpec struct {
	path string
	span string
}

func parseReadBundle(args []string) ([]readBundleSpec, int, error) {
	budget := 4000
	var operands []string
	optionsEnded, tokenOption, lineOption, tailOption := false, false, false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if optionsEnded || !strings.HasPrefix(arg, "-") {
			operands = append(operands, arg)
			continue
		}
		switch arg {
		case "--":
			optionsEnded = true
		case "--max-tokens":
			if tokenOption || i+1 == len(args) {
				return nil, 0, errors.New("--max-tokens requires one value and cannot repeat")
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > maxOutputTokens || strconv.Itoa(n) != args[i] {
				return nil, 0, errors.New("--max-tokens must be an integer from 1 through 15500")
			}
			budget, tokenOption = n, true
		case "-n":
			if lineOption || i+1 == len(args) {
				return nil, 0, errors.New("-n requires one value and cannot repeat")
			}
			i++
			n, err := strconv.ParseUint(args[i], 10, 64)
			if err != nil || n == 0 || n > 1<<53-1 || strconv.FormatUint(n, 10) != args[i] {
				return nil, 0, errors.New("-n must be a positive integer")
			}
			lineOption = true
		case "--tail":
			if tailOption {
				return nil, 0, errors.New("--tail cannot repeat")
			}
			tailOption = true
		default:
			return nil, 0, fmt.Errorf("unknown option %q", arg)
		}
	}
	var specs []readBundleSpec
	for _, operand := range operands {
		if readBundleRange.MatchString(operand) && len(specs) > 0 {
			if specs[len(specs)-1].span != "" {
				return nil, 0, errors.New("only one range may follow each path; prefix range-like paths with ./")
			}
			if _, _, err := parseReadBundleRange(operand); err != nil {
				return nil, 0, err
			}
			specs[len(specs)-1].span = operand
			continue
		}
		if operand == "" || len(specs) == 16 {
			return nil, 0, errors.New(readBundleUsage + " (1–16 files)")
		}
		specs = append(specs, readBundleSpec{path: operand})
	}
	if len(specs) == 0 {
		return nil, 0, errors.New(readBundleUsage)
	}
	if tailOption && !tokenOption && !lineOption {
		return nil, 0, errors.New("--tail requires -n or --max-tokens")
	}
	if len(specs) > 1 && (lineOption || tailOption) {
		return nil, 0, errors.New("-n and --tail require a single path")
	}
	return specs, budget, nil
}

var readBundleRange = regexp.MustCompile(`^[0-9]+:[0-9]+$`)

func parseReadBundleRange(span string) (uint64, uint64, error) {
	first, last, _ := strings.Cut(span, ":")
	start, startErr := strconv.ParseUint(first, 10, 64)
	end, endErr := strconv.ParseUint(last, 10, 64)
	if startErr != nil || endErr != nil || start > 1<<53-1 || end == 0 || end > 1<<53-1 ||
		strconv.FormatUint(start, 10) != first || strconv.FormatUint(end, 10) != last || start > end {
		return 0, 0, errors.New("line range must be START:END with canonical safe integers and START not after END")
	}
	return start, end, nil
}

// executeMCat owns the executable frontend's one AX observation. Single-file
// reads delegate unchanged argv to the generated tool. Multi-file composition
// delegates every source read to that same tool and only owns framing, budget
// allocation, and storage-before-exposure of per-file continuations.
func executeMCat(
	ctx context.Context,
	manifest toolWorkerManifest,
	runtime string,
	args []string,
	mcat toolContribution,
) (execution toolplugin.ExecutionOutput, err error) {
	journal := manifest.AXReadOutput
	if journal == "" {
		journal = os.Getenv(capturer.AXReadOutputEnvironment)
	}
	observation, observeErr := capturer.StartAXReadWithContext(
		journal,
		os.Getenv(shellruntime.ThreadIDEnvironment),
		"mcat",
		capturer.AXReadContext{},
	)
	var notices strings.Builder
	if observeErr != nil {
		fmt.Fprintf(&notices, "mcat: AX read evidence unavailable: %v\n", observeErr)
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
		if finishErr := observation.FinishResult(err == nil && execution.ExitCode == 0, class, exitCode); finishErr != nil {
			fmt.Fprintf(&notices, "mcat: AX read evidence incomplete: %v\n", finishErr)
		}
		execution.Stderr = notices.String() + execution.Stderr
	}()

	specs, budget, parseErr := parseReadBundle(args)
	if parseErr != nil {
		return toolplugin.ExecutionOutput{
			Stderr:       "mcat: " + parseErr.Error() + "\n",
			ExitCode:     1,
			FailureClass: "invalid_arguments",
		}, nil
	}
	if len(specs) == 1 {
		return toolplugin.Execute(
			ctx,
			manifest.NodeExecutable,
			runtime,
			mcat.Module,
			mcat.ModuleIndex,
			args,
			nil,
			"",
			nil,
		)
	}
	return executeMCatBundle(ctx, manifest, runtime, specs, budget, mcat)
}

func executeMCatBundle(
	ctx context.Context,
	manifest toolWorkerManifest,
	runtime string,
	specs []readBundleSpec,
	budget int,
	mcat toolContribution,
) (toolplugin.ExecutionOutput, error) {
	fail := func(message string, class string) (toolplugin.ExecutionOutput, error) {
		return toolplugin.ExecutionOutput{
			Stderr:       "mcat: " + message + "\n",
			ExitCode:     1,
			FailureClass: class,
		}, nil
	}
	reserve := 0
	for _, spec := range specs {
		// UTF-8/JSON escaped byte lengths conservatively bound framing tokens.
		// Keep the manifest ahead of bodies so every file has a visible receipt.
		reserve += len(mustMarshalJSON(spec.path))*2 + 512
	}
	if budget-reserve < len(specs) {
		return fail("budget cannot fit the bundle manifest; increase --max-tokens or select fewer paths", "invalid_arguments")
	}
	share := (budget - reserve) / len(specs)
	store, err := shellOutputStore(manifest)
	if err != nil {
		return toolplugin.ExecutionOutput{}, err
	}
	entries := make([]readBundleEntry, 0, len(specs))
	incomplete := false
	failureClass := ""
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return toolplugin.ExecutionOutput{}, err
		}
		execution, err := readMCatBundleFile(ctx, manifest, runtime, spec, share, mcat)
		if err != nil {
			return toolplugin.ExecutionOutput{}, err
		}
		state := "complete"
		omitted := "none"
		start := readBundleStart(spec)
		if execution.ExitCode != 0 {
			incomplete = true
			state = "failed"
			if failureClass == "" {
				failureClass = execution.FailureClass
			}
			if execution.FailureClass == "output_limit" {
				state, omitted = "incomplete", "unavailable"
			}
		}
		remainder := toolplugin.OmittedOutput{Stderr: execution.Stderr}
		if execution.OmittedOutput != nil {
			remainder.Stdout = execution.OmittedOutput.Stdout
			remainder.StdoutKind = execution.OmittedOutput.StdoutKind
			remainder.Stderr += execution.OmittedOutput.Stderr
			omitted = bundleRowSpan(start+bundleRowCount(execution.Stdout), remainder.Stdout)
		}
		handles, err := store.allocateHandles(ctx, 1)
		if err != nil {
			return toolplugin.ExecutionOutput{}, err
		}
		entries = append(entries, readBundleEntry{
			path: spec.path, start: start, shown: execution.Stdout, omitted: omitted, state: state,
			record: shellOutputRecord{Version: 1, ID: handles[0],
				Stdout: remainder.Stdout, Stderr: remainder.Stderr, StdoutKind: remainder.StdoutKind,
				StderrKind: remainder.StderrKind, ExitCode: execution.ExitCode},
		})
	}
	for _, entry := range entries {
		if entry.record.Stdout != "" || entry.record.Stderr != "" {
			if _, err := store.putReadRecord(ctx, entry.record); err != nil {
				return toolplugin.ExecutionOutput{}, err
			}
		}
	}
	status := 0
	if incomplete {
		status = 1
	}
	return toolplugin.ExecutionOutput{
		Stdout:       renderReadBundle(entries),
		ExitCode:     status,
		FailureClass: failureClass,
	}, nil
}

type readBundleEntry struct {
	path, shown, omitted, state string
	start                       uint64
	record                      shellOutputRecord
}

func renderReadBundle(entries []readBundleEntry) string {
	var manifest, bodies strings.Builder
	for i, entry := range entries {
		fmt.Fprintf(&manifest, "%d path=%s shown=%s omitted=%s status=%s", i+1,
			mustMarshalJSON(entry.path), bundleRowSpan(entry.start, entry.shown), entry.omitted, entry.state)
		if entry.record.Stdout != "" || entry.record.Stderr != "" {
			fmt.Fprintf(&manifest, " next_call=%q", "mread "+entry.record.ID)
		}
		manifest.WriteByte('\n')
		if entry.shown != "" {
			fmt.Fprintf(&bodies, "\n--- file %d ---\n%s", i+1, entry.shown)
		}
	}
	return manifest.String() + bodies.String()
}

func bundleRowCount(text string) uint64 {
	return uint64(strings.Count(text, "\n"))
}

func bundleRowSpan(start uint64, text string) string {
	count := bundleRowCount(text)
	if count == 0 {
		return "none"
	}
	return fmt.Sprintf("%d:%d", start, start+count-1)
}

func readBundleStart(spec readBundleSpec) uint64 {
	if spec.span == "" {
		return 1
	}
	parsed, _, _ := parseReadBundleRange(spec.span)
	return max(1, parsed)
}

func readMCatBundleFile(
	ctx context.Context,
	manifest toolWorkerManifest,
	runtime string,
	spec readBundleSpec,
	tokens int,
	mcat toolContribution,
) (toolplugin.ExecutionOutput, error) {
	args := []string{"--max-tokens", strconv.Itoa(tokens), "--", spec.path}
	if spec.span != "" {
		args = append(args, spec.span)
	}
	return toolplugin.Execute(
		ctx,
		manifest.NodeExecutable,
		runtime,
		mcat.Module,
		mcat.ModuleIndex,
		args,
		nil,
		"",
		nil,
	)
}
