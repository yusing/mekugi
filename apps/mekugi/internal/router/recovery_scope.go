package router

import (
	"context"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

func (s *mekugiReplayStore) recoverySession(ctx context.Context) (*recovery.Session, error) {
	s = s.scoped(ctx)
	return s.recovery.Attach(scope.Retained{Directory: s.directory, Namespace: s.session.Namespace, Thread: s.session.Thread}, s.retentionLimits(), s.writes)
}
