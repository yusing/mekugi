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

func openWaitStore(ctx context.Context, service *history.Service, retained *history.Session, thread string, store *history.Store, writes *persistence.Counter, limits history.Limits) error {
	retained.Close()
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		directory, err := history.ReplayDirectory()
		if err != nil {
			return err
		}
		store, err = service.Open(ctx, directory)
		limits = history.Limits{Total: history.DefaultStorageBytes, Kinds: map[string]int64{"commentary": 16 << 20}}
		if err != nil {
			return err
		}
	}
	selected, err := store.SelectSession(ctx, writes, limits, thread)
	if publication, ok := errors.AsType[*history.PublicationError](err); ok {
		err = requestexecution.StoragePublicationError(publication.Err)
	}
	if err != nil {
		return err
	}
	*retained = selected
	return nil
}

func waitTargetsPath(retained history.Session, workspace string, key [3]string) string {
	return filepath.Join(retained.Store.Directory(), waittargets.Name(workspace, key[0], key[1], key[2]))
}

func readWaitTargets(retained history.Session, workspace string, key [3]string) (targets []waittargets.Target, found bool, err error) {
	err = retained.Store.Transaction(retained.Context, retained.Writes, func(tx *history.Transaction) error {
		path := waitTargetsPath(retained, workspace, key)
		var err error
		targets, found, err = waittargets.Read(path)
		if err != nil || !found {
			return err
		}
		if err := requestexecution.StoragePublicationError(tx.Retain(retained.Thread, retained.Limits, filepath.Base(path))); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return targets, found, nil
}

func writeWaitTargets(retained history.Session, workspace string, key [3]string, targets []waittargets.Target) error {
	if retained.Store == nil {
		return nil
	}
	data, err := waittargets.Encode(targets)
	if err != nil {
		return err
	}
	return retained.Store.Transaction(retained.Context, retained.Writes, func(tx *history.Transaction) error {
		return writeRetainedRecord(tx, retained.Thread, retained.Limits, filepath.Base(waitTargetsPath(retained, workspace, key)), "wait-pending-", data)
	})
}
