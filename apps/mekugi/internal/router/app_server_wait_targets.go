package router

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	"github.com/yusing/mekugi/libraries/persistence"

	history "github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/modules/activity/waittargets"
)

func (u *appServerUI) openWaitStore(store *history.Store, writes *persistence.Counter, limits history.Limits) error {
	if u.waitRelease != nil {
		u.waitRelease()
		u.waitRelease = nil
	}
	ctx := u.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		directory, err := history.ReplayDirectory()
		if err != nil {
			return err
		}
		store, err = u.history.Open(ctx, directory)
		limits = history.Limits{Total: history.DefaultStorageBytes, Kinds: map[string]int64{"commentary": 16 << 20}}
		if err != nil {
			return err
		}
	}
	namespace, release, err := store.BeginSession(ctx, writes, limits, u.Thread)
	if publication, ok := errors.AsType[*history.PublicationError](err); ok {
		err = requestexecution.StoragePublicationError(publication.Err)
	}
	if err != nil {
		return err
	}
	u.session.waitStore, u.session.waitContext = store, ctx
	u.session.waitThread, u.session.waitWrites, u.session.waitLimits = u.Thread, writes, limits
	u.session.waitNamespace = namespace
	u.waitRelease = release
	return nil
}

func (s *appServerSession) waitTargetsPath(key [3]string) string {
	return filepath.Join(s.waitStore.Directory(), waittargets.Name(s.cwd, key[0], key[1], key[2]))
}

func (s *appServerSession) readWaitTargets(key [3]string) (targets []waittargets.Target, found bool, err error) {
	err = s.waitStore.Transaction(s.waitContext, s.waitWrites, func(tx *history.Transaction) error {
		path := s.waitTargetsPath(key)
		var err error
		targets, found, err = waittargets.Read(path)
		if err != nil || !found {
			return err
		}
		if err := requestexecution.StoragePublicationError(tx.Retain(s.waitThread, s.waitLimits, filepath.Base(path))); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return targets, found, nil
}

func (s *appServerSession) writeWaitTargets(key [3]string, targets []waittargets.Target) error {
	if s.waitStore == nil {
		return nil
	}
	data, err := waittargets.Encode(targets)
	if err != nil {
		return err
	}
	return s.waitStore.Transaction(s.waitContext, s.waitWrites, func(tx *history.Transaction) error {
		return writeRetainedRecord(tx, s.waitThread, s.waitLimits, filepath.Base(s.waitTargetsPath(key)), "wait-pending-", data)
	})
}
