package reports

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Service struct{ history *history.Service }

func New(history *history.Service) (*Service, error) {
	if history == nil {
		return nil, errors.New("command segments requires session history")
	}
	return &Service{history: history}, nil
}

// Scope is selected by the caller, never inferred from the process directory.
// Store is the existing publication handle, including its revision-aware size cache.
// An empty thread preserves uncatalogued publication when host identity is unknown.
type Scope struct {
	Store             *history.Store
	Workspace, Thread string
	Limits            history.Limits
	Writes            *persistence.Counter
}

func (s Scope) validate() error {
	if s.Store == nil || !filepath.IsAbs(s.Store.Directory()) || !filepath.IsAbs(s.Workspace) {
		return errors.New("command segment storage and workspace must be absolute")
	}
	return nil
}

func Name(workspace, id string) string {
	return fmt.Sprintf("segments-%x.json", sha256.Sum256(fmt.Appendf(nil, "%q:%q", workspace, id)))
}

func RecordKinds() []recordkind.Spec {
	return []recordkind.Spec{{ID: "commandsegments", MaxRecordBytes: history.MaxRecordBytes, Match: func(name string) bool {
		hash, ok := strings.CutPrefix(name, "segments-")
		hash, suffix := strings.CutSuffix(hash, ".json")
		return ok && suffix && validHash(hash)
	}}}
}

type record struct {
	Version   int
	Workspace string
	CallID    string
	Report    Report
}

type retained struct {
	report  *Report
	names   []string
	current *record
	legacy  *legacycall.Record
}

// read checks every present representation. Neither a good new report nor a
// usable historical projection can mask corruption in its neighbor.
func read(directory, workspace, id string) (retained, error) {
	var result retained
	if !IsID(id) {
		return result, errors.New("invalid command segment identity")
	}
	name := Name(workspace, id)
	data, err := history.ReadRecord(filepath.Join(directory, name))
	if err == nil {
		var value record
		if err := json.Unmarshal(data, &value, json.RejectUnknownMembers(true)); err != nil {
			return result, err
		}
		if value.Version != 1 || value.Workspace != workspace || value.CallID != id {
			return result, errors.New("command segment record identity/version mismatch")
		}
		if err := value.Report.validate(); err != nil {
			return result, err
		}
		result.current, result.report = &value, &value.Report
		result.names = append(result.names, name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	old, found, err := legacycall.Read(directory, workspace, id, false, history.MaxRecordBytes)
	if err != nil {
		return result, err
	}
	if !found {
		return result, nil
	}
	if old.History.CommandSegments == nil {
		return result, errors.New("historical command segment report is absent")
	}
	segments := old.History.CommandSegments
	report := Report{Command: segments.Command, Exit: segments.Exit, Output: segments.Output}
	for _, part := range segments.Parts {
		report.Parts = append(report.Parts, Part{Source: part.Source, Skipped: part.Skipped, Exit: part.Exit, Output: part.Output, Timing: Timing(part.Timing)})
	}
	if err := report.validate(); err != nil {
		return result, err
	}
	if result.report != nil && !result.report.equal(report) {
		return result, errors.New("conflicting durable command segment reports")
	}
	result.legacy = &old
	result.names = append(result.names, legacycall.Name(workspace, id, false))
	if result.report == nil {
		result.report = &report
	}
	return result, nil
}

// Publish retains ownership before returning. Equal historical-only reports are
// adopted in place, preserving their unrelated facts and referenced snapshots.
func (s *Service) Publish(ctx context.Context, scope Scope, id string, report Report) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if err := report.validate(); err != nil {
		return err
	}
	store := scope.Store
	return store.Transaction(ctx, scope.Writes, func(tx *history.Transaction) error {
		old, err := read(store.Directory(), scope.Workspace, id)
		if err != nil {
			return err
		}
		if old.report != nil {
			if !old.report.equal(report) {
				return errors.New("conflicting durable command segment report")
			}
			return tx.Retain(scope.Thread, scope.Limits, old.names...)
		}
		value := record{Version: 1, Workspace: scope.Workspace, CallID: id, Report: report}
		data, err := json.Marshal(&value, json.Deterministic(true))
		if err != nil {
			return err
		}
		name := Name(scope.Workspace, id)
		if err := tx.Retain(scope.Thread, scope.Limits, name); err != nil {
			return err
		}
		if err := tx.Admit(scope.Limits, name, int64(len(data))); err != nil {
			return err
		}
		return tx.Write(name, "segments-pending-", data)
	})
}

// Restore validates and adopts a batch in one publication snapshot. A malformed
// neighbor does not suppress independently valid reports. Callers bound batch
// size and lock waits to their presentation policy.
// Source: internal/router/command_segments.go:170:247@[cc2524a0] prepareCommandSegments.
func (s *Service) Restore(ctx context.Context, scope Scope, completed []Completed) (map[string]*Report, error) {
	result := make(map[string]*Report)
	if err := scope.validate(); err != nil {
		return result, err
	}
	store := scope.Store
	err := store.Transaction(ctx, scope.Writes, func(tx *history.Transaction) error {
		accepted := make(map[string]retained)
		var names []string
		for _, item := range completed {
			candidate, err := read(store.Directory(), scope.Workspace, item.ID)
			if err != nil || candidate.report == nil || !item.matches(*candidate.report) {
				continue
			}
			accepted[item.ID] = candidate
			names = append(names, candidate.names...)
		}
		if len(names) == 0 {
			return nil
		}
		if err := tx.Retain(scope.Thread, scope.Limits, names...); err == nil {
			for id, candidate := range accepted {
				result[id] = candidate.report
			}
		} else {
			// Preserve source order when only some new ownership fits the budget.
			for _, item := range completed {
				candidate, ok := accepted[item.ID]
				if ok && tx.Retain(scope.Thread, scope.Limits, candidate.names...) == nil {
					result[item.ID] = candidate.report
				}
			}
		}
		return nil
	})
	return result, err
}

// Inspection selects an actual retained envelope. Legacy is present only for
// historical-only evidence; Data is the canonical new envelope otherwise.
// Dependencies describes the selected envelope, not the union of both forms.
type Inspection struct {
	Name         string
	Data         []byte
	Legacy       *legacycall.Record
	Dependencies []string
}

// Inspect never creates storage, acquires locks, or adopts ownership.
func (s *Service) Inspect(directory, workspace, id string) (Inspection, bool, error) {
	var result Inspection
	if !filepath.IsAbs(directory) || !filepath.IsAbs(workspace) {
		return result, false, errors.New("command segment storage and workspace must be absolute")
	}
	retained, err := read(directory, workspace, id)
	if err != nil || retained.report == nil {
		return result, false, err
	}
	result.Name = retained.names[0]
	if retained.current != nil {
		result.Data, err = json.Marshal(retained.current, json.Deterministic(true))
	} else {
		result.Legacy = retained.legacy
		result.Dependencies, err = s.history.Attach(directory).Dependencies(result.Name)
	}
	return result, err == nil, err
}
