// Package replaytiming defines optional retained provider timing for replay.
package replaytiming

import (
	"context"
	"time"
)

type Interval struct {
	Thread     string
	Start, End time.Time
}
type Reader func(context.Context, string, map[string]bool, func(Interval) error) error
type Definition[S any] struct {
	Read func(S, context.Context, string, map[string]bool, func(Interval) error) error
}
