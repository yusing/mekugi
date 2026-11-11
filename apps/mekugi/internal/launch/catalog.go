// Source: apps/mekugi/cmd/mekugi/catalog.go:1:153@[ebde9ad5]
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// bundledModelCatalog bounds the same offline metadata request for every caller.
func bundledModelCatalog(ctx context.Context, executable, directory string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "debug", "models", "--bundled")
	cmd.Dir = directory
	cmd.WaitDelay = 5 * time.Second
	// Do not forward diagnostics from the bootstrap command into the TUI or
	// include configuration/authentication details in a startup error.
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("prepare Codex model catalog: %w", err)
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("prepare Codex model catalog: %w", ctx.Err())
		}
		return nil, fmt.Errorf("start codex debug models: %w", err)
	}
	const maxCatalogBytes = 8 << 20
	body, readErr := io.ReadAll(io.LimitReader(stdout, maxCatalogBytes+1))
	if readErr != nil || len(body) > maxCatalogBytes {
		cancel()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("read Codex model catalog: %w", readErr)
	}
	if len(body) > maxCatalogBytes {
		return nil, errors.New("codex model catalog exceeds 8 MiB")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("prepare Codex model catalog: %w", err)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("codex debug models failed: %w; check that Codex supports this command and its catalog configuration is valid", waitErr)
	}
	return body, nil
}
