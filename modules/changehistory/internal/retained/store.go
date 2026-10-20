package retained

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type store struct {
	publication *history.Store
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
	ReviewFiles []diffmodel.ReviewFile
}
type capturedRecord struct {
	legacycall.Record
	History capturedHistory
}

func (s *store) read(workspace, call string, commentary bool) (capturedRecord, bool, error) {
	old, found, err := legacycall.Read(s.directory, workspace, call, commentary, history.MaxRecordBytes)
	if err != nil || !found {
		return capturedRecord{}, found, err
	}
	projected := capturedHistory{History: old.History}
	for _, file := range old.History.ReviewFiles {
		projected.ReviewFiles = append(projected.ReviewFiles, diffmodel.ReviewFile(file))
	}
	return capturedRecord{Record: old, History: projected}, true, nil
}
func authoredChangeHistory(value capturedHistory) capturedHistory {
	coverage := ""
	if value.ExecOutcome != nil {
		coverage = value.ExecOutcome.Coverage
	}
	value.ReviewFiles, coverage = changes.AuthoredEvidence(value.ReviewFiles, coverage)
	if value.ExecOutcome != nil && value.ExecOutcome.Coverage != coverage {
		outcome := *value.ExecOutcome
		outcome.Coverage = coverage
		value.ExecOutcome = &outcome
	}
	return value
}
