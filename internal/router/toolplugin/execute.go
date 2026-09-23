package toolplugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type hostProcessGroupKey struct{}
type frontendOrphanCleanupKey struct{}

// WithHostProcessGroup keeps executable frontend descendants in Codex's
// command group so stock cancellation can terminate the whole invocation.
func WithHostProcessGroup(ctx context.Context) context.Context {
	return context.WithValue(ctx, hostProcessGroupKey{}, true)
}

func hostProcessGroupOwned(ctx context.Context) bool {
	owned, _ := ctx.Value(hostProcessGroupKey{}).(bool)
	return owned
}

// EnableFrontendOrphanCleanup is for a dedicated authenticated frontend
// process, not a router or test process that owns unrelated child commands.
func EnableFrontendOrphanCleanup(ctx context.Context) (context.Context, error) {
	if err := enableFrontendSubreaper(); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, frontendOrphanCleanupKey{}, true), nil
}

// JSON can encode each byte as a six-byte Unicode escape. The additional
// allowance covers the execution envelope.
const maxEncodedExecutionHostOutputBytes = 6*(ExecutionOutputBudgetBytes+16<<20) + 1<<20

// executionResponse carries private host cleanup metadata separately from the
// output returned to the executor caller.
type executionResponse struct {
	ExecutionOutput
	TerminationReason string `json:"terminationReason,omitempty"`
}

func Execute(
	ctx context.Context,
	node, runtimeRoot, module string,
	index int,
	arguments []string,
	stdin *os.File,
	directory string,
	environment []string,
) (ExecutionOutput, error) {
	request := struct {
		Operation         string   `json:"operation"`
		SnapshotRoot      string   `json:"snapshotRoot"`
		Module            string   `json:"module"`
		Index             int      `json:"index"`
		Arguments         []string `json:"arguments"`
		InputFD           bool     `json:"inputFD"`
		OutputBudgetBytes int      `json:"outputBudgetBytes"`
	}{
		Operation:         "execute",
		SnapshotRoot:      filepath.Join(runtimeRoot, snapshotDirectory),
		Module:            module,
		Index:             index,
		Arguments:         arguments,
		InputFD:           stdin != nil,
		OutputBudgetBytes: ExecutionOutputBudgetBytes,
	}
	var result executionResponse
	var scriptFiles []*os.File
	if stdin != nil {
		scriptRead, scriptWrite, err := os.Pipe()
		if err != nil {
			return ExecutionOutput{}, fmt.Errorf("create plugin script pipe: %w", err)
		}
		scriptFiles = []*os.File{scriptRead, scriptWrite}
		defer func() {
			_ = scriptRead.Close()
			_ = scriptWrite.Close()
		}()
	}
	err := invoke(
		ctx,
		node,
		filepath.Join(runtimeRoot, hostFilename),
		"",
		directory,
		environment,
		maxEncodedExecutionHostOutputBytes,
		!hostProcessGroupOwned(ctx),
		stdin,
		scriptFiles,
		request,
		&result,
	)
	return result.ExecutionOutput, err
}

// Formatting batches share one tokenizer startup, not executable plugin state.
const maxFormatOutputBatchSize = 3

// The whole batch shares the existing single-execution source and output bounds.
const maxFormatOutputBatchBytes = ExecutionOutputBudgetBytes

// FormatOutput loads only the shared tokenizer, not executable tool declarations
// or the WASM source-analysis core. It owns no workspace or inherited input.
func FormatOutput(ctx context.Context, node, runtimeRoot string, arguments []string) (ExecutionOutput, error) {
	if formatter := formatterFromContext(ctx, node, runtimeRoot); formatter != nil {
		return formatOutputReused(ctx, formatter, arguments)
	}
	request := struct {
		Operation    string   `json:"operation"`
		SnapshotRoot string   `json:"snapshotRoot"`
		Arguments    []string `json:"arguments"`
	}{"format-output", filepath.Join(runtimeRoot, snapshotDirectory), arguments}
	var result ExecutionOutput
	err := invoke(ctx, node, filepath.Join(runtimeRoot, hostFilename), request.SnapshotRoot,
		"", nil, maxEncodedExecutionHostOutputBytes, false, nil, nil, request, &result)
	return result, err
}

// FormatOutputBatch formats up to three candidates in order in a single one-shot
// host. Aggregate arguments and encoded responses retain single-call byte bounds.
func FormatOutputBatch(ctx context.Context, node, runtimeRoot string, arguments [][]string) ([]ExecutionOutput, error) {
	if len(arguments) == 0 || len(arguments) > maxFormatOutputBatchSize {
		return nil, fmt.Errorf("formatting batch must contain 1 to %d candidates", maxFormatOutputBatchSize)
	}
	bytes := 0
	for _, candidate := range arguments {
		if len(candidate) != 4 {
			return nil, fmt.Errorf("formatting candidate must contain four arguments")
		}
		for _, argument := range candidate {
			if len(argument) > maxFormatOutputBatchBytes-bytes {
				return nil, fmt.Errorf("formatting batch arguments exceed %d bytes", maxFormatOutputBatchBytes)
			}
			bytes += len(argument)
		}
	}
	if formatter := formatterFromContext(ctx, node, runtimeRoot); formatter != nil {
		return formatOutputBatchReused(ctx, formatter, arguments)
	}
	request := struct {
		Operation    string     `json:"operation"`
		SnapshotRoot string     `json:"snapshotRoot"`
		Arguments    [][]string `json:"arguments"`
	}{"format-output-batch", filepath.Join(runtimeRoot, snapshotDirectory), arguments}
	var response []*struct {
		Stdout   *string `json:"stdout"`
		Stderr   string  `json:"stderr"`
		ExitCode *int    `json:"exitCode"`
	}
	if err := invoke(ctx, node, filepath.Join(runtimeRoot, hostFilename), request.SnapshotRoot,
		"", nil, maxEncodedExecutionHostOutputBytes, false, nil, nil, request, &response); err != nil {
		return nil, err
	}
	if len(response) != len(arguments) {
		return nil, fmt.Errorf("formatting batch returned %d results for %d candidates", len(response), len(arguments))
	}
	results := make([]ExecutionOutput, len(response))
	for i, result := range response {
		if result == nil || result.Stdout == nil || result.ExitCode == nil {
			return nil, fmt.Errorf("formatting batch result %d is incomplete", i)
		}
		results[i] = ExecutionOutput{Stdout: *result.Stdout, Stderr: result.Stderr, ExitCode: *result.ExitCode}
	}
	return results, nil
}
