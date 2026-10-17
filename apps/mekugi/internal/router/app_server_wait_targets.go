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
	u.retainedSession.Close()
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
	selected, err := store.SelectSession(ctx, writes, limits, u.Thread)
	if publication, ok := errors.AsType[*history.PublicationError](err); ok {
		err = requestexecution.StoragePublicationError(publication.Err)
	}
	if err != nil {
		return err
	}
	u.retainedSession = selected
	return nil
}

func (u *appServerUI) waitTargetsPath(key [3]string) string {
	return filepath.Join(u.retainedSession.Store.Directory(), waittargets.Name(u.attached.cwd, key[0], key[1], key[2]))
}

func (u *appServerUI) readWaitTargets(key [3]string) (targets []waittargets.Target, found bool, err error) {
	err = u.retainedSession.Store.Transaction(u.retainedSession.Context, u.retainedSession.Writes, func(tx *history.Transaction) error {
		path := u.waitTargetsPath(key)
		var err error
		targets, found, err = waittargets.Read(path)
		if err != nil || !found {
			return err
		}
		if err := requestexecution.StoragePublicationError(tx.Retain(u.retainedSession.Thread, u.retainedSession.Limits, filepath.Base(path))); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return targets, found, nil
}

func (u *appServerUI) writeWaitTargets(key [3]string, targets []waittargets.Target) error {
	if u.retainedSession.Store == nil {
		return nil
	}
	data, err := waittargets.Encode(targets)
	if err != nil {
		return err
	}
	return u.retainedSession.Store.Transaction(u.retainedSession.Context, u.retainedSession.Writes, func(tx *history.Transaction) error {
		return writeRetainedRecord(tx, u.retainedSession.Thread, u.retainedSession.Limits, filepath.Base(u.waitTargetsPath(key)), "wait-pending-", data)
	})
}
