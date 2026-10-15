package read

import (
	"context"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

func executeBundle(
	ctx context.Context,
	specs []Selection,
	budget int,
	numbered bool,
) (command.Output, error) {
	fail := func(message string, class string) (command.Output, error) {
		return command.Output{
			Stderr:       "mcat: " + message + "\n",
			ExitCode:     1,
			FailureClass: class,
		}, nil
	}
	codec, err := tokenizer.New()
	if err != nil {
		return command.Output{}, err
	}
	reserve := 16
	for _, spec := range specs {
		pathTokens, err := codec.Count(quoteBundlePath(spec.Path))
		if err != nil {
			return command.Output{}, err
		}
		reserve += pathTokens*2 + 48
	}
	if budget-reserve < len(specs) {
		return fail("budget cannot fit the bundle manifest; increase --max-tokens or select fewer paths", "invalid_arguments")
	}
	share := (budget - reserve) / len(specs)
	executions := make([]command.Output, len(specs))
	unused, pending := budget-reserve-share*len(specs), 0
	for i, spec := range specs {
		execution, err := scan(ctx, spec, commandargs.ReaderOptions{Budget: share, Number: numbered})
		if err != nil {
			return command.Output{}, err
		}
		executions[i] = execution
		if execution.ExitCode == 0 {
			used, err := codec.Count(execution.Stdout)
			if err != nil {
				return command.Output{}, err
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
			// Redistribute over the captured logical rows, never a second source read.
			all := execution.Stdout + execution.OmittedOutput.Stdout
			limit := share + unused/pending
			candidate := all
			if len(candidate) > limit*128 {
				candidate = candidate[:strings.LastIndexByte(candidate[:limit*128], '\n')+1]
			}
			shown, err := codec.SelectRows(candidate, limit, false)
			if err != nil {
				return command.Output{}, err
			}
			execution = command.Output{Stdout: shown}
			if len(shown) < len(all) {
				execution.ExitCode, execution.FailureClass = 1, "output_limit"
				execution.OmittedOutput = &command.OmittedOutput{Stdout: all[len(shown):], StdoutKind: "rows"}
			}
			executions[i] = execution
		}
	}
	entries := make([]readBundleEntry, 0, len(specs))
	var diagnostics strings.Builder
	incomplete := false
	failureClass := ""
	for i, spec := range specs {
		if err := ctx.Err(); err != nil {
			return command.Output{}, err
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
		// Keep source diagnostics visible with one path prefix; only omitted
		// output needs a retained continuation.
		for line := range strings.Lines(execution.Stderr) {
			if execution.FailureClass == "output_limit" && strings.HasPrefix(line, "mcat: output incomplete:") {
				continue
			}
			diagnostics.WriteString(prefixReadBundleDiagnostic(spec.Path, line))
		}
		var remainder command.OmittedOutput
		if execution.OmittedOutput != nil {
			remainder = *execution.OmittedOutput
			omitted = bundleRowSpan(start+bundleRowCount(execution.Stdout), remainder.Stdout)
		}
		entries = append(entries, readBundleEntry{
			path: spec.Path, start: start, shown: execution.Stdout, omitted: omitted, state: state,
			recovery: command.RecoveryOutput{ReferenceIndex: i, ExitCode: execution.ExitCode, OmittedOutput: command.OmittedOutput{
				Stdout: remainder.Stdout, Stderr: remainder.Stderr, StdoutKind: remainder.StdoutKind,
				StderrKind: remainder.StderrKind, SourceRow: start + bundleRowCount(execution.Stdout)}},
		})
	}
	plan := &bundleDelivery{entries: entries, budget: budget, output: command.Output{Stderr: diagnostics.String(), FailureClass: failureClass}}
	if incomplete {
		plan.output.ExitCode = 1
	}
	prepared, err := plan.prepare(nil)
	if err != nil {
		return command.Output{}, err
	}
	output := prepared.Unreferenced
	// Reserve one slot per input: final framing can make any source incomplete.
	if len(prepared.Recovery) != 0 {
		output.Delivery = plan
	}
	return output, nil
}

type readBundleEntry struct {
	path, shown, omitted, state string
	start                       uint64
	recovery                    command.RecoveryOutput
}

// renderReadBundle lists a manifest row only for files whose body alone does
// not prove a complete read: failed, incomplete, empty, or continued files.
// Every body header names its path and shown range.
func renderReadBundle(entries []readBundleEntry) string {
	var manifest, bodies strings.Builder
	for i, entry := range entries {
		path, shown := quoteBundlePath(entry.path), bundleRowSpan(entry.start, entry.shown)
		continued := entry.recovery.Stdout != "" || entry.recovery.Stderr != ""
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
// preserving the generated reader's quoted path when already present.
func prefixReadBundleDiagnostic(path string, stderr string) string {
	if stderr == "" {
		return ""
	}
	prefix := "mcat: " + quoteBundlePath(path) + ": "
	var out strings.Builder
	for line := range strings.Lines(stderr) {
		if strings.HasPrefix(line, "mcat: \"") {
			out.WriteString(line)
		} else {
			out.WriteString(prefix)
			out.WriteString(strings.TrimPrefix(line, "mcat: "))
		}
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

func readBundleStart(spec Selection) uint64 {
	return uint64(max(1, spec.start))
}
