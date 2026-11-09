package retained

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/evidence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type store struct {
	publication *history.Store
	readRecord  func(string, int64) ([]byte, error)
	directory   string
	session     scope.Retained
	limits      history.Limits
	writes      *persistence.Counter
	transaction *history.Transaction
	updates     *[]changes.Update
}

func selectedStore(selected changes.Scope) *store {
	return &store{publication: selected.Store, directory: selected.Store.Directory(), session: scope.Retained{Namespace: selected.Namespace, Thread: selected.Thread}, limits: selected.Limits, writes: selected.Writes}
}
func (s *store) handleNamespace() string { return s.session.Namespace }
func (s *store) locked(ctx context.Context, fn func(*store) error) error {
	if s.transaction != nil {
		return errors.New("nested change publication transaction")
	}
	return s.publication.Transaction(ctx, s.writes, func(tx *history.Transaction) error {
		owned := *s
		owned.transaction = tx
		return fn(&owned)
	})
}
func (s *store) retainFiles(names ...string) error {
	return publicationError(s.transaction.Retain(s.session.Thread, s.limits, names...))
}
func (s *store) maintainStorage(name string, size int64) error {
	return publicationError(s.transaction.Admit(s.limits, name, size))
}
func publicationError(err error) error {
	if err == nil {
		return nil
	}
	return &changes.PublicationError{Err: err}
}

// The index preserves its historical legacy spelling, including unescaped HTML.
func marshalIndex(value any) ([]byte, error) {
	return json.Marshal(value, jsonv1.DefaultOptionsV1(), jsontext.EscapeForHTML(false))
}

type capturedHistory struct {
	legacycall.History
	ReviewFiles []evidence.Review
}
type capturedRecord struct {
	legacycall.Record
	History capturedHistory
}

func (s *store) read(workspace, call string, commentary bool) (capturedRecord, bool, error) {
	old, found, err := history.ReadHostRecord(s.directory, workspace, call, commentary)
	if err != nil || !found {
		return capturedRecord{}, found, err
	}
	projected := capturedHistory{History: old.History}
	source := &evidence.Source{Store: s.publication, Read: s.readRecord}
	root, err := evidence.Root(old)
	if err != nil {
		return capturedRecord{}, false, err
	}
	if root == "" {
		projected.ReviewFiles, err = evidence.Reviews(source, old)
		if err != nil {
			return capturedRecord{}, false, err
		}
	} else {
		manifest, err := evidence.Open(source, root)
		if err != nil {
			return capturedRecord{}, false, err
		}
		projected.ReviewFiles, err = evidence.ReadReviews(source, manifest.Reviews)
		if err != nil {
			return capturedRecord{}, false, err
		}
		if len(manifest.Observation) != 0 {
			observation := new(legacycall.Observation)
			if err := json.Unmarshal(manifest.Observation, observation, json.RejectUnknownMembers(true)); err != nil {
				return capturedRecord{}, false, err
			}
			files, err := evidence.ReadFiles(source, manifest.Files)
			if err != nil {
				return capturedRecord{}, false, err
			}
			for _, file := range files {
				observation.Files = append(observation.Files, legacycall.File{Path: file.Path, Kind: string(file.Kind), Link: file.Link})
			}
			projected.ExecObservation = observation
		}
	}
	return capturedRecord{Record: old.Record, History: projected}, true, nil
}
func authoredChangeHistory(value capturedHistory) capturedHistory {
	coverage := ""
	if value.ExecOutcome != nil {
		coverage = value.ExecOutcome.Coverage
	}
	value.ReviewFiles, coverage = changes.AuthoredReviews(value.ReviewFiles, coverage)
	if value.ExecOutcome != nil && value.ExecOutcome.Coverage != coverage {
		outcome := *value.ExecOutcome
		outcome.Coverage = coverage
		value.ExecOutcome = &outcome
	}
	return value
}
