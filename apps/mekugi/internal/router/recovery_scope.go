package router

import (
	"context"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

func retainedScope(ctx context.Context, directory string) scope.Retained {
	identity, _ := ctx.Value(storageSessionKey{}).(storageSessionIdentity)
	return scope.Retained{Directory: directory, Namespace: identity.Namespace, Thread: identity.Thread}
}
func (s *mekugiReplayStore) recoverySession(ctx context.Context) (*recovery.Session, error) {
	s = s.scoped(ctx)
	return s.recovery.Attach(scope.Retained{Directory: s.directory, Namespace: s.session.Namespace, Thread: s.session.Thread}, s.retentionLimits(), s.writes)
}
