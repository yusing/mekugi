package history

import (
	"encoding/hex"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
)

// Source: internal/router/storage_retention.go:38:43@b8ca48c7 retainedSession.
type Catalog struct {
	Version  int             `json:"version"`
	Thread   string          `json:"thread"`
	LastUsed time.Time       `json:"last_used"`
	Files    map[string]bool `json:"files"`
}

// Only Retain borrows this immutable decoded view, under the store lock.
// Its revision is independent of the file-size cache's revision.
type retainedCatalog struct {
	name     string
	revision string
	value    Catalog
}

// CatalogName recognizes the session owner's existing on-disk catalog format.
func CatalogName(name string) bool {
	hash, ok := strings.CutPrefix(name, "session-")
	if !ok {
		return false
	}
	hash, ok = strings.CutSuffix(hash, ".json")
	if !ok || len(hash) != 64 || strings.ToLower(hash) != hash {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func (s *Store) registered(name string) bool {
	_, found, err := s.service.Kind(name)
	return err == nil && found
}

// ReadCatalog preserves unregistered names without reading their records.
func (s *Store) ReadCatalog(name string) (Catalog, error) {
	var catalog Catalog
	data, err := ReadRecord(filepath.Join(s.directory, name))
	if err != nil {
		return catalog, err
	}
	if json.Unmarshal(data, &catalog, jsonv1.DefaultOptionsV1()) != nil || catalog.Version != 1 || catalog.Thread == "" || catalog.LastUsed.IsZero() || catalog.Files == nil || SessionName(catalog.Thread) != name {
		return catalog, errors.New("invalid session retention record")
	}
	for file := range catalog.Files {
		if file == "" || filepath.Base(file) != file || file == "." || file == ".." {
			return catalog, errors.New("invalid session retention file identity")
		}
	}
	return catalog, nil
}

// Dependencies delegates record meaning to its registered owner. A not-yet
// published record has no stored dependencies; callers retain prepared ones.
func (s *Store) Dependencies(name string) ([]string, error) {
	kind, found, err := s.service.Kind(name)
	if err != nil || !found || kind.Dependencies == nil {
		return nil, err
	}
	data, err := ReadRecord(filepath.Join(s.directory, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names, err := kind.Dependencies(recordkind.Record{Name: name, Data: data})
	if err != nil {
		return nil, err
	}
	for _, dependency := range names {
		if !s.registered(dependency) {
			return nil, errors.New("unregistered retained dependency")
		}
	}
	return names, nil
}

// Retain adopts dependencies under the same publication transaction, before
// returning a reference. Catalog touch coalescing does not change ownership.
// Source: internal/router/storage_retention.go:207:255@b8ca48c7 retainFiles.
func (tx *Transaction) Retain(thread string, limits Limits, names ...string) error {
	if err := tx.check(); err != nil {
		return err
	}
	s := tx.store
	if thread == "" {
		return nil
	}
	// A fork, retained change selection or recovered output adopts the call's
	// shared snapshots together with its envelope, under the same store lock.
	names = slices.Clone(names)
	for _, name := range slices.Clone(names) {
		dependencies, err := s.Dependencies(name)
		if err != nil {
			return err
		}
		names = append(names, dependencies...)
	}
	name := SessionName(thread)
	revision, err := tx.Revision()
	if err != nil {
		s.catalog = nil
		return err
	}
	if s.catalog != nil && (s.catalog.name != name || s.catalog.revision != revision) {
		s.catalog = nil
	}
	var session Catalog
	if s.catalog != nil {
		session = s.catalog.value
	} else {
		session, err = s.ReadCatalog(name)
		if errors.Is(err, os.ErrNotExist) {
			session = Catalog{Version: 1, Thread: thread, Files: make(map[string]bool)}
		} else if err != nil {
			return err
		} else {
			s.catalog = &retainedCatalog{name: name, revision: revision, value: session}
		}
	}
	changed := session.LastUsed.IsZero()
	filesOwned := changed
	for _, file := range names {
		if !s.registered(file) {
			return errors.New("cannot retain an invalid storage file identity")
		}
		if !session.Files[file] {
			if !filesOwned {
				session.Files = maps.Clone(session.Files)
				filesOwned = true
			}
			changed = true
			session.Files[file] = true
		}
	}
	if !changed && time.Since(session.LastUsed) < time.Minute {
		return nil
	}
	session.LastUsed = time.Now().UTC()
	data, err := json.Marshal(&session, json.Deterministic(true))
	if err != nil {
		return err
	}
	if len(data) > MaxRecordBytes {
		return capacityError("session ownership catalog", int64(len(data)), MaxRecordBytes, "Continue in a new chat; the current session has too many retained references to fit in one catalog record.")
	}
	if len(names) > 0 {
		// Admission cannot prune. The store lock bridges validation to this
		// publication; failed catalog growth therefore needs no rollback.
		if err := tx.Admit(limits, name, int64(len(data))); err != nil {
			return err
		}
	}
	if err := tx.Write(name, "session-pending-", data); err != nil {
		return err
	}
	// Cache publication is auxiliary: a revision-read failure cannot turn a
	// successful durable publication into a failure.
	if revision, err := tx.Revision(); err == nil {
		s.catalog = &retainedCatalog{name: name, revision: revision, value: session}
	}
	return nil
}

// Only this owner's exact coordination vocabulary is exempt from unknown-record
// protection. Other unregistered leaf entries can own retained dependencies.
func coordinationName(name string) bool {
	switch name {
	case "store.lock", "storage-revision", "retention-pressure", "retention-sweep", "retention-snapshot.lock", "retention-maintenance.lock", "capture-order":
		return true
	}
	if tail, ok := strings.CutSuffix(name, ".lock"); ok && CatalogName(tail+".json") {
		return true
	}
	if hash, ok := strings.CutPrefix(name, "handle-scope-"); ok {
		return CatalogName("session-" + hash + ".json")
	}
	return false
}
