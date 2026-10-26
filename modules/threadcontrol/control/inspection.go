package control

import (
	"context"
	"errors"
)

// RetainWaitMetrics freezes the exact dispatched continuation in its existing
// wait checkpoint so uncertain delivery can reconcile after restart.
func (s *Service) RetainWaitMetrics(ctx context.Context, thread, id string, metrics map[string]SessionMetrics) error {
	snapshot, err := detach(metrics)
	if err != nil {
		return err
	}
	return s.update(ctx, thread, func(_ *Tree, member *Member) error {
		if member.Wait == nil || member.Wait.ID != id || member.Wait.State != "dispatching" {
			return errors.New("wait metrics require the claimed continuation")
		}
		if member.Wait.Metrics != nil {
			return unchanged
		}
		member.Wait.Metrics = snapshot
		return nil
	})
}
