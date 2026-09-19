//go:build !linux && !darwin

package router

import (
	"context"
	"fmt"

	"mvdan.cc/sh/v3/interp"
)

func executeShellUlimit(ctx context.Context, args []string) error {
	fmt.Fprintln(interp.HandlerCtx(ctx).Stderr, "ulimit: inspection is unavailable on this platform")
	return interp.ExitStatus(2)
}
