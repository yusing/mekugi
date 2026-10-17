package router

import (
	"context"
	"path/filepath"

	history "github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/modules/activity/waittargets"
)

func (u *appServerUI) openWaitStore(store *mekugiReplayStore) error {
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
		store, err = openReplayStore(ctx, u.history, nil, directory)
		if err != nil {
			return err
		}
	}
	ctx, release, err := store.beginSession(ctx, u.Thread, "")
	if err != nil {
		return err
	}
	u.session.waitStore, u.session.waitContext = store.scoped(ctx), ctx
	u.waitRelease = release
	return nil
}

func (s *appServerSession) waitTargetsPath(key [3]string) string {
	return filepath.Join(s.waitStore.directory, waittargets.Name(s.cwd, key[0], key[1], key[2]))
}

func (s *appServerSession) readWaitTargets(key [3]string) (targets []waittargets.Target, found bool, err error) {
	err = s.waitStore.locked(s.waitContext, func(store *mekugiReplayStore) error {
		path := s.waitTargetsPath(key)
		var err error
		targets, found, err = waittargets.Read(path)
		if err != nil || !found {
			return err
		}
		if err := store.retainFiles(filepath.Base(path)); err != nil {
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
	return s.waitStore.locked(s.waitContext, func(store *mekugiReplayStore) error {
		return store.writeManagedFile(filepath.Base(s.waitTargetsPath(key)), "wait-pending-", data)
	})
}
