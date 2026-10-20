// Source: internal/router/session_ui_replay_read.go:444:465@[3e523196] optional timing evidence.
package capture

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/yusing/mekugi/contracts/replaytiming"
)

// readTimingLines bounds all input, rejects devices/FIFOs, and reports malformed
// evidence rather than silently presenting a partial session as complete.
func readTimingLines(ctx context.Context, path string, visit func([]byte) error) error {
	f, err := openCaptureEvidence(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bounded := &io.LimitedReader{R: f, N: (64 << 20) + 1}
	s := bufio.NewScanner(bounded)
	s.Buffer(make([]byte, 64<<10), 16<<20)
	for line := 1; s.Scan(); line++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(s.Bytes()); err != nil {
			return fmt.Errorf("%s:%d: %w", path, line, err)
		}
	}
	if bounded.N == 0 {
		return errors.New("replay input grew beyond 64 MiB")
	}
	return s.Err()
}

func (*Service) ReplayTiming(ctx context.Context, path string, loaded map[string]bool, observe func(replaytiming.Interval) error) error {
	return readTimingLines(ctx, path, func(line []byte) error {
		var p struct {
			Boundary string    `json:"boundary"`
			Thread   string    `json:"thread_id"`
			At       time.Time `json:"captured_at"`
			Duration int64     `json:"duration_ms"`
		}
		if err := json.Unmarshal(line, &p); err != nil {
			return err
		}
		if p.Boundary != "provider" || !loaded[p.Thread] {
			return nil
		}
		if p.At.IsZero() || p.Duration < 0 || p.Duration > 86400000 {
			return errors.New("invalid provider timing")
		}
		return observe(replaytiming.Interval{Thread: p.Thread, Start: p.At.Add(-time.Duration(p.Duration) * time.Millisecond), End: p.At})
	})
}
