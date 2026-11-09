package control

import (
	"context"
	"errors"
)

// YieldCursor borrows immutable counts; acknowledgement replaces the map.
func (s *Service) YieldCursor(thread, contextID string) map[string]int {
	member, ok := s.member(thread)
	if !ok || contextID == "" || member.YieldContext != contextID {
		return nil
	}
	return member.YieldSeen
}

func (s *Service) RetainWaitProgress(ctx context.Context, thread, id, contextID string, progress WaitProgress, seen map[string]int) error {
	frozen, err := detach(struct {
		Progress WaitProgress
		Seen     map[string]int
	}{progress, seen})
	if err != nil {
		return err
	}
	return s.update(ctx, thread, func(_ *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.ID != id || wait.State != "dispatching" || !wait.Yielded {
			return errors.New("wait progress requires the claimed yield")
		}
		if wait.Progress != nil {
			return unchanged
		}
		wait.Progress, wait.YieldSeen, wait.YieldContext = &frozen.Progress, frozen.Seen, contextID
		return nil
	})
}

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
