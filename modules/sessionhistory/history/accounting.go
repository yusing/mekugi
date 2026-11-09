package history

import (
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/persistence"
)

// Publication owns these totals, not each process's inventory cache. A peer
// needs only this small revision-bound record and the replacement's old size.
// The revision is invalidated before effects and accounting is committed before
// unlocking. An interrupted publisher therefore requires recovery, never an
// admission based on stale totals.
type storageAccounting struct {
	Version  int              `json:"version"`
	Revision string           `json:"revision"`
	Kinds    []string         `json:"kinds"`
	Bytes    map[string]int64 `json:"bytes"`
}

func (tx *Transaction) accountingView(rebuild bool) (*storageAccounting, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if tx.accounting != nil {
		return tx.accounting, nil
	}
	s := tx.store
	revision, err := tx.Revision()
	if err != nil {
		return nil, err
	}
	kinds := make([]string, 0, len(s.service.kinds))
	for _, kind := range s.service.kinds {
		kinds = append(kinds, kind.ID)
	}
	slices.Sort(kinds)
	data, err := ReadRecord(filepath.Join(s.directory, "storage-accounting"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var accounting storageAccounting
	if err == nil {
		if err := json.Unmarshal(data, &accounting, json.RejectUnknownMembers(true)); err != nil {
			return nil, err
		}
		if accounting.Version != 1 || accounting.Bytes == nil {
			return nil, errors.New("invalid storage accounting record")
		}
		for kind, size := range accounting.Bytes {
			if size < 0 || kind != "" && !slices.Contains(accounting.Kinds, kind) {
				return nil, errors.New("invalid storage accounting total")
			}
		}
		if accounting.Revision == revision && slices.Equal(accounting.Kinds, kinds) {
			tx.accounting = &accounting
			return tx.accounting, nil
		}
	}
	if !rebuild {
		// Unbudgeted coordination writes must not acquire an unrelated
		// inventory or fail because that inventory needs repair. Leave its
		// revision stale for the next actual capacity admission to recover.
		return nil, nil
	}
	// Migration, changed record owners, or incomplete publication is the only
	// reason admission reconstructs totals. Normal peer writes maintain them.
	if _, err := tx.FileSizes(); err != nil {
		return nil, err
	}
	accounting = storageAccounting{Version: 1, Revision: revision, Kinds: kinds, Bytes: make(map[string]int64)}
	for name, size := range s.files.sizes {
		kind, _, err := s.service.Kind(name)
		if err != nil {
			return nil, err
		}
		accounting.Bytes[kind.ID] += size
	}
	tx.accounting, tx.accountingDirty = &accounting, true
	return tx.accounting, nil
}

func (tx *Transaction) commitAccounting() error {
	if tx.accounting == nil || !tx.accountingDirty && !tx.revised {
		return nil
	}
	revision, err := tx.Revision()
	if err != nil {
		return err
	}
	tx.accounting.Revision = revision
	data, err := json.Marshal(tx.accounting, json.Deterministic(true))
	if err != nil {
		return err
	}
	return persistence.AtomicFile(filepath.Join(tx.store.directory, "storage-accounting"), "accounting-pending-", data, tx.writes)
}

func (s *Store) accountingUsage(accounting *storageAccounting, kind recordkind.Spec) storageUsage {
	u := storageUsage{kind: kind, own: accounting.Bytes[kind.ID], total: accounting.Bytes[""]}
	if kind.ID == "" {
		u.own = 0
	}
	for _, registered := range s.service.kinds {
		if !registered.SeparateBudget {
			u.total += accounting.Bytes[registered.ID]
		}
	}
	if kind.SeparateBudget {
		u.total = u.own
	}
	return u
}

func (s *Store) recordSize(name string) (int64, error) {
	info, err := os.Lstat(filepath.Join(s.directory, name))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("managed record is not a regular file")
	}
	return info.Size(), nil
}

// remove keeps maintenance's bounded retirement batches in the same accounting
// protocol as replacement. A partial batch retains the sizes of its actual effects.
func (tx *Transaction) remove(name string) error {
	if err := tx.check(); err != nil {
		return err
	}
	s := tx.store
	kind, managed, err := s.service.Kind(name)
	if err != nil {
		return err
	}
	size, err := s.recordSize(name)
	if err != nil {
		return err
	}
	accounting, err := tx.accountingView(false)
	if err != nil {
		return err
	}
	if err := tx.advanceRevision(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		tx.accounting = nil
		return err
	}
	if accounting != nil && (managed || CatalogName(name)) {
		accounting.Bytes[kind.ID] -= size
	}
	return nil
}
