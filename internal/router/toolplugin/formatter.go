package toolplugin

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

type outputFormatterContextKey struct{}

type outputFormatter struct {
	node, root string
	ctx        context.Context
	cancel     context.CancelFunc
	gate       chan struct{}
	process    *hostProcess // protected by gate
}

func WithOutputFormatter(ctx context.Context, node, runtimeRoot string) (context.Context, func()) {
	lifetime, cancel := context.WithCancel(ctx)
	formatter := &outputFormatter{
		node: node, root: runtimeRoot, ctx: lifetime, cancel: cancel, gate: make(chan struct{}, 1),
	}
	return context.WithValue(ctx, outputFormatterContextKey{}, formatter), formatter.Close
}

func formatterFromContext(ctx context.Context, node, root string) *outputFormatter {
	formatter, _ := ctx.Value(outputFormatterContextKey{}).(*outputFormatter)
	if formatter != nil && formatter.node == node && formatter.root == root {
		return formatter
	}
	return nil
}

func (f *outputFormatter) call(ctx context.Context, request, response any) error {
	select {
	case f.gate <- struct{}{}:
		defer func() { <-f.gate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
	if err := f.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.process != nil {
		select {
		case <-f.process.done:
			f.process.stop()
			f.process = nil
		default:
		}
	}
	if f.process == nil {
		if err := f.start(ctx); err != nil {
			return err
		}
	}
	if err := f.process.exchange(ctx, request, response); err != nil {
		f.process.stop()
		f.process = nil
		return err
	}
	return nil
}

func (f *outputFormatter) start(ctx context.Context) error {
	snapshotRoot := filepath.Join(f.root, snapshotDirectory)
	process, err := startHost(ctx, f.ctx, f.node, filepath.Join(f.root, hostFilename),
		"--format-server", snapshotRoot, maxEncodedExecutionHostOutputBytes)
	if err != nil {
		return err
	}
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err := process.exchange(ctx, struct {
		SnapshotRoot string `json:"snapshotRoot"`
	}{snapshotRoot}, &ready); err != nil {
		process.stop()
		return err
	}
	if !ready.Ready {
		process.stop()
		return errors.New("plugin formatter did not become ready")
	}
	f.process = process
	return nil
}

func (f *outputFormatter) Close() {
	if f == nil {
		return
	}
	f.cancel()
	f.gate <- struct{}{}
	defer func() { <-f.gate }()
	if f.process != nil {
		f.process.stop()
		f.process = nil
	}
}

func formatOutputReused(ctx context.Context, formatter *outputFormatter, arguments []string) (ExecutionOutput, error) {
	var result ExecutionOutput
	err := formatter.call(ctx, struct {
		Operation string   `json:"operation"`
		Arguments []string `json:"arguments"`
	}{"format-output", arguments}, &result)
	return result, err
}

func formatOutputBatchReused(ctx context.Context, formatter *outputFormatter, arguments [][]string) ([]ExecutionOutput, error) {
	var response []*struct {
		Stdout   *string `json:"stdout"`
		Stderr   string  `json:"stderr"`
		ExitCode *int    `json:"exitCode"`
	}
	if err := formatter.call(ctx, struct {
		Operation string     `json:"operation"`
		Arguments [][]string `json:"arguments"`
	}{"format-output-batch", arguments}, &response); err != nil {
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
