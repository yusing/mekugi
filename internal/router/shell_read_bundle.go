package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/shellruntime"
	"github.com/yusing/mekugi/internal/verifiedrow"
	"mvdan.cc/sh/v3/interp"
)

const readBundleUsage = "hcat [--max-tokens N] PATH [START:END] [PATH [START:END] ...]"

type readBundleSpec struct {
	path string
	span string
}

func parseReadBundle(args []string) ([]readBundleSpec, int, error) {
	budget := 4000
	var operands []string
	optionsEnded, tokenOption, singleFileOptions := false, false, false
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
			if err != nil || n < 1 || n > hrunMaxTokens || strconv.Itoa(n) != args[i] {
				return nil, 0, errors.New("--max-tokens must be an integer from 1 through 15500")
			}
			budget, tokenOption = n, true
		case "-n", "--preview-bytes":
			singleFileOptions = true
			if i+1 == len(args) {
				return nil, 0, errors.New(arg + " requires a value")
			}
			i++
		case "--tail":
			singleFileOptions = true
		case "--batch":
			return nil, 0, errors.New("--batch is no longer needed; list paths and optional ranges directly")
		default:
			operands = append(operands, arg)
		}
	}
	var specs []readBundleSpec
	for _, operand := range operands {
		if readBundleRange.MatchString(operand) && len(specs) > 0 {
			if specs[len(specs)-1].span != "" {
				return nil, 0, errors.New("only one range may follow each path; prefix range-like paths with ./")
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
	if len(specs) > 1 && singleFileOptions {
		return nil, 0, errors.New("-n, --tail and --preview-bytes require a single path")
	}
	return specs, budget, nil
}

var readBundleRange = regexp.MustCompile(`^[0-9]+:[0-9]+$`)

// Bundle composition delegates source parsing, verified rows and row admission to
// hcat. Only framing and allocation belong here; omitted rows use the mread store.
func executeReadBundle(ctx context.Context, manifest toolWorkerManifest, runtime string, specs []readBundleSpec, budget int, hcat toolContribution, shellID string) error {
	handler := interp.HandlerCtx(ctx)
	fail := func(err error) error {
		_, _ = fmt.Fprintf(handler.Stderr, "hcat: %v\n", err)
		return interp.ExitStatus(1)
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
	var entries []readBundleEntry
	incomplete := false
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		execution, err := readBundleFile(ctx, manifest, runtime, spec, share, hcat, shellID)
		if err != nil {
			return fail(err)
		}
		state := "complete"
		omitted := "none"
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
		handles, err := store.allocateHandles(ctx, 1)
		if err != nil {
			return fail(err)
		}
		entries = append(entries, readBundleEntry{
			path: spec.path, shown: execution.Stdout, omitted: omitted, state: state,
			record: shellOutputRecord{Version: 1, ID: handles[0],
				Stdout: remainder.Stdout, Stderr: remainder.Stderr, StdoutKind: remainder.StdoutKind,
				StderrKind: remainder.StderrKind, ExitCode: execution.ExitCode},
		})
	}
	// Only the exact outer capture is display output. Files, pipes and command
	// substitutions retain the requested reader budget and their original bytes.
	writer, direct := handler.Stdout.(*shellOutputWriter)
	var stream *shellDisplayStream
	if direct {
		stream, _ = writer.destination.(*shellDisplayStream)
	}
	output := renderReadBundle(entries)
	if stream != nil {
		// Match the capture -> display lock order used by ordinary writes.
		writer.capture.mu.Lock()
		defer writer.capture.mu.Unlock()
		stream.owner.mu.Lock()
		defer stream.owner.mu.Unlock()
		limit := min(budget, stream.owner.remaining)
		fits := func(outputs ...string) ([]bool, error) {
			arguments := make([][]string, len(outputs))
			for i, output := range outputs {
				arguments[i] = []string{strconv.Itoa(max(1, limit)), "shell", output, ""}
			}
			selected, err := toolplugin.FormatOutputBatch(ctx, manifest.NodeExecutable, runtime, arguments)
			if err != nil {
				return nil, err
			}
			fits := make([]bool, len(outputs))
			for i, result := range selected {
				var selection struct {
					Text *string `json:"text"`
				}
				if result.ExitCode != 0 || json.Unmarshal([]byte(result.Stdout), &selection) != nil || selection.Text == nil {
					return nil, errors.New("invalid bundle output selection")
				}
				fits[i] = limit > 0 && *selection.Text == outputs[i]
			}
			return fits, nil
		}
		ok, err := fits(output, renderReadBundle(trimReadBundle(entries, 0)))
		if err != nil {
			return err
		}
		// If even the manifest cannot fit, preserve normal outer retention
		// rather than changing execution into a display-budget failure.
		if !ok[0] && ok[1] {
			lower, upper := 0, 0
			for _, entry := range entries {
				upper = max(upper, len(strings.SplitAfter(entry.shown, "\n")))
			}
			// Evaluate two binary-search levels in one tokenizer host. At most
			// three candidates are rendered, and only the original branch path
			// is followed; unused speculative results cannot change the ceiling.
			for lower+1 < upper {
				middle := lower + (upper-lower)/2
				ceilings := []int{middle}
				if lower+1 < middle {
					ceilings = append(ceilings, lower+(middle-lower)/2)
				}
				if middle+1 < upper {
					ceilings = append(ceilings, middle+(upper-middle)/2)
				}
				outputs := make([]string, len(ceilings))
				for i, ceiling := range ceilings {
					outputs[i] = renderReadBundle(trimReadBundle(entries, ceiling))
				}
				ok, err = fits(outputs...)
				if err != nil {
					return err
				}
				for range 2 {
					if lower+1 >= upper {
						break
					}
					middle = lower + (upper-lower)/2
					if ok[slices.Index(ceilings, middle)] {
						lower = middle
					} else {
						upper = middle
					}
				}
			}
			entries = trimReadBundle(entries, lower)
			output = renderReadBundle(entries)
		}
	}
	for _, entry := range entries {
		if entry.record.Stdout != "" || entry.record.Stderr != "" {
			if _, err := store.putReadRecord(ctx, entry.record); err != nil {
				return err
			}
		}
	}
	if stream != nil {
		// Bypass only the two already-held mutexes, not capture or display limits.
		if _, err := writer.writeLocked([]byte(output), stream.writeLocked); err != nil {
			return err
		}
	} else if _, err := io.WriteString(handler.Stdout, output); err != nil {
		return err
	}
	if incomplete {
		return interp.ExitStatus(1)
	}
	return nil
}

type readBundleEntry struct {
	path, shown, omitted, state string
	record                      shellOutputRecord
}

func trimReadBundle(entries []readBundleEntry, rows int) []readBundleEntry {
	result := slices.Clone(entries)
	for i := range result {
		entry := &result[i]
		end := 0
		for range rows {
			next := strings.IndexByte(entry.shown[end:], '\n')
			if next < 0 {
				end = len(entry.shown)
				break
			}
			end += next + 1
		}
		if end == len(entry.shown) {
			continue
		}
		entry.record.Stdout = entry.shown[end:] + entry.record.Stdout
		entry.record.StdoutKind = "rows"
		entry.shown = entry.shown[:end]
		entry.omitted = bundleRowSpan(entry.record.Stdout)
		if entry.state == "complete" {
			entry.state = "incomplete"
		}
	}
	return result
}

func renderReadBundle(entries []readBundleEntry) string {
	var manifest, bodies strings.Builder
	for i, entry := range entries {
		fmt.Fprintf(&manifest, "%d path=%s shown=%s omitted=%s status=%s", i+1,
			mustMarshalJSON(entry.path), bundleRowSpan(entry.shown), entry.omitted, entry.state)
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
	args := []string{"--max-tokens", strconv.Itoa(tokens), "--", spec.path}
	if spec.span != "" {
		args = append(args, spec.span)
	}
	return toolplugin.Execute(ctx, manifest.NodeExecutable, runtime, hcat.Module, hcat.ModuleIndex,
		args, nil, handler.Dir, shellEnvironment(handler.Env))
}
