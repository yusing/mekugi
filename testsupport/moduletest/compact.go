package moduletest

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"strings"

	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// StoreCompact creates a historical summary record for compatibility fixtures.
func StoreCompact(ctx context.Context, store changes.Scope, thread, summary, reload string) (string, error) {
	if store.Store == nil || thread == "" || strings.TrimSpace(summary) == "" {
		return "", errors.New("compaction publication requires a retained thread and summary")
	}
	_, err := compact.ReadCommands(reload)
	if err != nil {
		return "", err
	}
	namespace, release, err := store.Store.BeginSession(ctx, store.Writes, store.Limits, thread)
	if err != nil {
		return "", err
	}
	defer release()
	var reference string
	err = store.Store.Transaction(ctx, store.Writes, func(tx *history.Transaction) error {
		handles, err := tx.ReserveHandles(namespace, 1)
		if err != nil {
			return err
		}
		reference = compact.Prefix + namespace + ":" + handles[0]
		data, err := json.Marshal(compact.Record{Version: 1, Reference: reference, Summary: summary, Selection: reload})
		if err != nil {
			return err
		}
		name := compact.Name(reference)
		if err := tx.Admit(store.Limits, name, int64(len(data))); err != nil {
			return err
		}
		if err := tx.Write(name, "compact-pending-", data); err != nil {
			return err
		}
		return tx.Retain(thread, store.Limits, name)
	})
	if err != nil {
		return "", err
	}
	return reference, nil
}
