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
	"github.com/yusing/mekugi/internal/tokenizer"
)

const readBundleUsage = "mcat [-n N] [--max-tokens N] [--tail] PATH [START:END ...] [PATH [START:END ...] ...]"

type readBundleSpec struct {
	path string
	span string
}

func parseReadBundle(args []string) ([]readBundleSpec, int, error) {
	budget := 6000
	var operands []string
	optionsEnded, tokenOption, lineOption, tailOption := false, false, false, false
	for i := 0; i < len(args); i++ {
		if !optionsEnded && strings.HasPrefix(args[i], "--max-tokens=") {
			args = append(append([]string(nil), args[:i]...), expandMaxTokensOption(args[i:])...)
		}
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
				return nil, 0, errors.New(maxTokensArgumentError)
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 || n > maxOutputTokens || strconv.Itoa(n) != args[i] {
				return nil, 0, errors.New(maxTokensArgumentError)
			}
			budget, tokenOption = n, true
		case "-n":
			if lineOption || i+1 == len(args) {
				return nil, 0, errors.New("-n requires one value and cannot repeat")
			}
			i++
			if readBundleRange.MatchString(args[i]) {
				return nil, 0, fmt.Errorf("-n takes a row count, not a range; retry: mcat PATH %s", strings.ReplaceAll(args[i], "-", ":"))
			}
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
		if readBundleRange.MatchString(operand) {
			if len(specs) == 0 {
				return nil, 0, fmt.Errorf("range-like paths require ./; retry: %s", workerCommand("mcat", []string{"./" + operand}))
			}
			operand = strings.ReplaceAll(operand, "-", ":")
			if _, _, err := parseReadBundleRange(operand); err != nil {
				return nil, 0, err
			}
			if specs[len(specs)-1].span == "" {
				specs[len(specs)-1].span = operand
			} else {
				if len(specs) == 16 {
					return nil, 0, errors.New(readBundleUsage + " (1–16 reads)")
				}
				specs = append(specs, readBundleSpec{path: specs[len(specs)-1].path, span: operand})
			}
			continue
		}
		if correction := correctedReadRange(operand); correction != "" {
			path := "PATH"
			if len(specs) > 0 {
				path = specs[len(specs)-1].path
			}
			return nil, 0, fmt.Errorf("invalid range %q; retry: %s", operand, workerCommand("mcat", []string{path, correction}))
		}
		if path, row, ok := strings.CutLast(operand, ":"); ok && path != "" && readRowNumber.MatchString(row) {
			if _, err := os.Stat(operand); errors.Is(err, os.ErrNotExist) {
				return nil, 0, fmt.Errorf("ranges must be separate operands; retry: %s", workerCommand("mcat", []string{path, row + ":" + row}))
			}
		}
		if operand == "" || len(specs) == 16 {
			return nil, 0, errors.New(readBundleUsage + " (1–16 reads)")
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

var readBundleRange = regexp.MustCompile(`^[0-9]+[:-][0-9]+$`)
var readRowNumber = regexp.MustCompile(`^[0-9]+$`)
var readMalformedRange = regexp.MustCompile(`^([0-9]+)(:\+|,)([0-9]+)$`)

func correctedReadRange(operand string) string {
	if readRowNumber.MatchString(operand) {
		return operand + ":" + operand
	}
	parts := readMalformedRange.FindStringSubmatch(operand)
	if parts == nil {
		return ""
	}
	if parts[2] == "," {
		return parts[1] + ":" + parts[3]
	}
	start, err := strconv.ParseUint(parts[1], 10, 64)
	count, countErr := strconv.ParseUint(parts[3], 10, 64)
	if err != nil || countErr != nil || count == 0 || start > 1<<53-1 || count > 1<<53-max(1, start) {
		return parts[1] + ":END"
	}
	return fmt.Sprintf("%d:%d", max(1, start), max(1, start)+count-1)
}

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
		os.Getenv(codexThreadIDEnvironment),
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
		execution, err := toolplugin.Execute(
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
		if err == nil && execution.OmittedOutput != nil {
			start := readBundleStart(specs[0])
			shown, omitted := bundleRowCount(execution.Stdout), bundleRowCount(execution.OmittedOutput.Stdout)
			execution.OmittedOutput.SourceRow = start + shown
			shownStart := start
			for _, arg := range args {
				if arg == "--" {
					break
				}
				if arg == "--tail" {
					execution.OmittedOutput.SourceRow = start
					shownStart = start + omitted
					break
				}
			}
			if budget != 6000 {
				execution.OmittedOutput.MaxTokens = budget
			}
			var notices strings.Builder
			for line := range strings.Lines(execution.Stderr) {
				if !strings.HasPrefix(line, "mcat: output incomplete:") {
					notices.WriteString(line)
				}
			}
			fmt.Fprintf(&notices, "mcat: shown %s of %d rows (%d-token limit)\n", bundleRowSpan(shownStart, execution.Stdout), shown+omitted, budget)
			execution.Stderr = notices.String()
		}
		return execution, err
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
	codec, err := tokenizer.New()
	if err != nil {
		return toolplugin.ExecutionOutput{}, err
	}
	reserve := 16
	for _, spec := range specs {
		pathTokens, err := codec.Count(string(mustMarshalJSON(spec.path)))
		if err != nil {
			return toolplugin.ExecutionOutput{}, err
		}
		reserve += pathTokens*2 + 48
	}
	if budget-reserve < len(specs) {
		return fail("budget cannot fit the bundle manifest; increase --max-tokens or select fewer paths", "invalid_arguments")
	}
	share := (budget - reserve) / len(specs)
	executions := make([]toolplugin.ExecutionOutput, len(specs))
	unused, pending := budget-reserve-share*len(specs), 0
	for i, spec := range specs {
		execution, err := readMCatBundleFile(ctx, manifest, runtime, spec, share, mcat)
		if err != nil {
			return toolplugin.ExecutionOutput{}, err
		}
		executions[i] = execution
		if execution.ExitCode == 0 {
			used, err := codec.Count(execution.Stdout)
			if err != nil {
				return toolplugin.ExecutionOutput{}, err
			}
			unused += max(0, share-used)
		} else if execution.OmittedOutput != nil {
			pending++
		}
	}
	if pending > 0 && unused >= pending {
		for i, execution := range executions {
			if execution.OmittedOutput == nil {
				continue
			}
			execution, err := readMCatBundleFile(ctx, manifest, runtime, specs[i], share+unused/pending, mcat)
			if err != nil {
				return toolplugin.ExecutionOutput{}, err
			}
			executions[i] = execution
		}
	}
	store, err := shellOutputStore(manifest)
	if err != nil {
		return toolplugin.ExecutionOutput{}, err
	}
	entries := make([]readBundleEntry, 0, len(specs))
	var diagnostics strings.Builder
	incomplete := false
	failureClass := ""
	for i, spec := range specs {
		if err := ctx.Err(); err != nil {
			return toolplugin.ExecutionOutput{}, err
		}
		execution := executions[i]
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
		// Source diagnostics are short and path-free, so they stay visible with
		// a path prefix; only omitted output needs a retained continuation.
		for line := range strings.Lines(execution.Stderr) {
			if execution.FailureClass == "output_limit" && strings.HasPrefix(line, "mcat: output incomplete:") {
				continue
			}
			diagnostics.WriteString(prefixReadBundleDiagnostic(spec.path, line))
		}
		var remainder toolplugin.OmittedOutput
		if execution.OmittedOutput != nil {
			remainder = *execution.OmittedOutput
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
				StderrKind: remainder.StderrKind, ExitCode: execution.ExitCode, SourceRow: start + bundleRowCount(execution.Stdout)},
		})
	}
	render := func() string {
		text := renderReadBundle(entries)
		var handles []string
		for _, entry := range entries {
			if entry.record.Stdout != "" || entry.record.Stderr != "" {
				handles = append(handles, entry.record.ID)
			}
		}
		if len(handles) != 0 {
			limit := 0
			if budget != 6000 {
				limit = budget
			}
			text += "\n" + strings.TrimPrefix(readNextCall(strings.Join(handles, " "), limit), "read: incomplete; ")
		}
		return text
	}
	// Verify the actual framing, including handles, rather than trusting the reserve.
	for {
		used, err := codec.Count(render())
		if err != nil {
			return toolplugin.ExecutionOutput{}, err
		}
		if used <= budget {
			break
		}
		trimmed := false
		for i := len(entries) - 1; i >= 0; i-- {
			entry := &entries[i]
			if entry.shown == "" {
				continue
			}
			end := strings.LastIndex(entry.shown[:len(entry.shown)-1], "\n") + 1
			entry.record.Stdout = entry.shown[end:] + entry.record.Stdout
			entry.record.StdoutKind = "rows"
			entry.shown = entry.shown[:end]
			entry.record.SourceRow = entry.start + bundleRowCount(entry.shown)
			entry.omitted = bundleRowSpan(entry.record.SourceRow, entry.record.Stdout)
			entry.state, entry.record.ExitCode = "incomplete", 1
			incomplete, failureClass, trimmed = true, "output_limit", true
			break
		}
		if !trimmed {
			return fail("budget cannot fit the bundle manifest; increase --max-tokens or select fewer paths", "invalid_arguments")
		}
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
		Stdout:       render(),
		Stderr:       diagnostics.String(),
		ExitCode:     status,
		FailureClass: failureClass,
	}, nil
}

type readBundleEntry struct {
	path, shown, omitted, state string
	start                       uint64
	record                      shellOutputRecord
}

// renderReadBundle lists a manifest row only for files whose body alone does
// not prove a complete read: failed, incomplete, empty, or continued files.
// Every body header names its path and shown range.
func renderReadBundle(entries []readBundleEntry) string {
	var manifest, bodies strings.Builder
	for i, entry := range entries {
		path, shown := mustMarshalJSON(entry.path), bundleRowSpan(entry.start, entry.shown)
		continued := entry.record.Stdout != "" || entry.record.Stderr != ""
		if entry.state != "complete" || entry.shown == "" || entry.omitted != "none" || continued {
			fmt.Fprintf(&manifest, "%d path=%s shown=%s omitted=%s status=%s", i+1, path, shown, entry.omitted, entry.state)
			manifest.WriteByte('\n')
		}
		if entry.shown != "" {
			fmt.Fprintf(&bodies, "\n--- file %d path=%s shown=%s ---\n%s", i+1, path, shown, entry.shown)
		}
	}
	if manifest.Len() == 0 {
		return strings.TrimPrefix(bodies.String(), "\n")
	}
	return manifest.String() + bodies.String()
}

// prefixReadBundleDiagnostic names the source path on each diagnostic line,
// replacing the generated reader's own "mcat: " prefix when present.
func prefixReadBundleDiagnostic(path string, stderr string) string {
	if stderr == "" {
		return ""
	}
	prefix := "mcat: " + string(mustMarshalJSON(path)) + ": "
	var out strings.Builder
	for line := range strings.Lines(stderr) {
		out.WriteString(prefix)
		out.WriteString(strings.TrimPrefix(line, "mcat: "))
	}
	if !strings.HasSuffix(stderr, "\n") {
		out.WriteByte('\n')
	}
	return out.String()
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
