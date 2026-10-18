//go:build !pprof

package launch

import (
	"context"
	"io"
)

func startProfiling(_ context.Context, _ io.Writer) (func() error, error) {
	return func() error { return nil }, nil
}
