package toolplugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

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
		stdin,
		scriptFiles,
		request,
		&result,
	)
	return result.ExecutionOutput, err
}

// FormatOutput loads only the shared tokenizer, not executable tool declarations
// or the WASM source-analysis core. It owns no workspace or inherited input.
func FormatOutput(ctx context.Context, node, runtimeRoot string, arguments []string) (ExecutionOutput, error) {
	request := struct {
		Operation    string   `json:"operation"`
		SnapshotRoot string   `json:"snapshotRoot"`
		Arguments    []string `json:"arguments"`
	}{"format-output", filepath.Join(runtimeRoot, snapshotDirectory), arguments}
	var result ExecutionOutput
	err := invoke(ctx, node, filepath.Join(runtimeRoot, hostFilename), request.SnapshotRoot,
		"", nil, maxEncodedExecutionHostOutputBytes, nil, nil, request, &result)
	return result, err
}

type CommandRouting struct {
	Executable string   `json:"executable"`
	Commands   []string `json:"commands"`
}

func LoadCommandRouting(ctx context.Context, node, runtimeRoot string) (CommandRouting, error) {
	ctx, cancel := context.WithTimeout(ctx, pluginInvocationTimeout)
	defer cancel()
	request := struct {
		Operation    string `json:"operation"`
		SnapshotRoot string `json:"snapshotRoot"`
	}{"command-routing", filepath.Join(runtimeRoot, snapshotDirectory)}
	var result CommandRouting
	err := invoke(ctx, node, filepath.Join(runtimeRoot, hostFilename), request.SnapshotRoot,
		"", nil, 128<<10, nil, nil, request, &result)
	return result, err
}

// RewriteCommand maps argv without executing commands or interpreting shell source.
func RewriteCommand(ctx context.Context, node, runtimeRoot, directory string, environment, arguments []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, pluginInvocationTimeout)
	defer cancel()
	request := struct {
		Operation    string   `json:"operation"`
		SnapshotRoot string   `json:"snapshotRoot"`
		Arguments    []string `json:"arguments"`
	}{"rewrite-command", filepath.Join(runtimeRoot, snapshotDirectory), arguments}
	var result struct {
		Arguments []string `json:"arguments"`
	}
	err := invoke(ctx, node, filepath.Join(runtimeRoot, hostFilename), request.SnapshotRoot,
		directory, environment, 128<<10, nil, nil, request, &result)
	return result.Arguments, err
}
