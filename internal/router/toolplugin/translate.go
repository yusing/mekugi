package toolplugin

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

const pluginInvocationTimeout = 5 * time.Second

func Translate(ctx context.Context, node, runtimeRoot, module string, index int, input string) (Translation, error) {
	ctx, cancel := context.WithTimeout(ctx, pluginInvocationTimeout)
	defer cancel()
	request := struct {
		Operation    string `json:"operation"`
		SnapshotRoot string `json:"snapshotRoot"`
		Module       string `json:"module"`
		Index        int    `json:"index"`
		Input        string `json:"input"`
	}{
		Operation:    "translate",
		SnapshotRoot: filepath.Join(runtimeRoot, snapshotDirectory),
		Module:       module,
		Index:        index,
		Input:        input,
	}
	var result Translation
	err := invoke(
		ctx,
		node,
		filepath.Join(runtimeRoot, hostFilename),
		filepath.Join(runtimeRoot, snapshotDirectory),
		"",
		nil,
		ExecutionOutputBudgetBytes,
		nil,
		nil,
		request,
		&result,
	)
	if errors.Is(err, context.DeadlineExceeded) {
		return Translation{}, fmt.Errorf("plugin translation exceeded %s", pluginInvocationTimeout)
	}
	return result, err
}
