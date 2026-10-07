//go:build !pprof

package router

import (
	"context"
	"io"
)

func startProfiling(_ context.Context, _ io.Writer) (func() error, error) {
	return func() error { return nil }, nil
}
