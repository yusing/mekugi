package router

import "context"

// The router supplies invocation settings and notice routing. Sessionhistory
// owns catalogs, planning, leases, and every bounded retirement commit.
func (s *mekugiReplayStore) cleanupSessions(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s = s.scoped(ctx)
	return s.publication.Cleanup(ctx, s.writes, s.retentionLimits(), s.session.Thread, func(phase, message string) {
		if s.storageNotice != nil {
			s.storageNotice("", "", phase, message)
		}
	})
}
