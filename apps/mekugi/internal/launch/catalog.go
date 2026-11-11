// Source: apps/mekugi/cmd/mekugi/catalog.go:1:153@[ebde9ad5]
package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// bundledModelCatalog bounds the same offline metadata request for every caller.
func bundledModelCatalog(ctx context.Context, executable string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Join(filepath.Dir(executable), "mekugi-models.json"))
	if err != nil {
		return nil, fmt.Errorf("open native model catalog: %w", err)
	}
	defer file.Close()
	const maxCatalogBytes = 8 << 20
	body, readErr := io.ReadAll(io.LimitReader(file, maxCatalogBytes+1))
	if readErr != nil {
		return nil, fmt.Errorf("read Codex model catalog: %w", readErr)
	}
	if len(body) > maxCatalogBytes {
		return nil, errors.New("codex model catalog exceeds 8 MiB")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("prepare Codex model catalog: %w", err)
	}
	return body, nil
}
