package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/contracts/reference"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/outputrecovery/internal/record"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Service owns recovery semantics; history owns publication and retained identity.
type Service struct {
	history *history.Service
	changes *reference.Resolver
}

func New(history *history.Service, resolvers []reference.Resolver) (*Service, error) {
	if history == nil {
		return nil, errors.New("output recovery requires session history")
	}
	service := &Service{history: history}
	for _, resolver := range resolvers {
		if resolver.Field != "changes" || resolver.Canonicalize == nil || resolver.Resolve == nil || resolver.Dependencies == nil {
			return nil, fmt.Errorf("invalid reference resolver %q", resolver.Field)
		}
		if service.changes != nil {
			return nil, errors.New("duplicate changes reference resolver")
		}
		service.changes = new(resolver)
	}
	return service, nil
}

func DefaultLimits() history.Limits {
	return history.Limits{Total: history.DefaultStorageBytes}
}

// Session is one explicitly selected retained scope. It owns no lease or process.
// The frontend holds the session lease through delivery.
type Session struct {
	service *Service
	scope   scope.Retained
	store   *history.Store
	limits  history.Limits
	writes  *persistence.Counter
}

func (service *Service) Attach(selected scope.Retained, limits history.Limits, writes *persistence.Counter) (*Session, error) {
	return service.AttachStore(service.history.Attach(selected.Directory), selected.Namespace, selected.Thread, limits, writes)
}

// AttachStore borrows the caller's selected history store without acquiring a lease.
// The caller retains authentication and lease ownership through delivery.
func (service *Service) AttachStore(store *history.Store, namespace, thread string, limits history.Limits, writes *persistence.Counter) (*Session, error) {
	if store == nil || store.Directory() == "" {
		return nil, errors.New("read recovery storage is unavailable")
	}
	selected := scope.Retained{Directory: store.Directory(), Namespace: namespace, Thread: thread}
	info, err := os.Lstat(filepath.Join(selected.Directory, "store.lock"))
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("read recovery storage is missing or invalid")
	}
	return &Session{service: service, scope: selected, store: store, limits: limits, writes: writes}, nil
}

func (s *Session) Reserve(ctx context.Context, count int) ([]string, error) {
	var handles []string
	err := s.store.Transaction(ctx, s.writes, func(tx *history.Transaction) error {
		var err error
		handles, err = tx.ReserveHandles(s.scope.Namespace, count)
		return err
	})
	return handles, err
}

func (s *Session) Publish(ctx context.Context, output command.OmittedOutput, exitCode int) (string, error) {
	handles, err := s.Reserve(ctx, 1)
	if err != nil {
		return "", err
	}
	return s.PublishAt(ctx, handles[0], output, exitCode)
}

func (s *Session) PublishAt(ctx context.Context, id string, output command.OmittedOutput, exitCode int) (string, error) {
	return s.put(ctx, record.Record{Version: 1, ID: id, Stdout: output.Stdout, Stderr: output.Stderr, ExitCode: exitCode, StdoutKind: output.StdoutKind, StderrKind: output.StderrKind, SourceRow: output.SourceRow})
}

// PublishSelectionAt retains an evidence owner's canonical selection at an
// already reserved identity. The registered resolver owns its private schema.
func (s *Session) PublishSelectionAt(ctx context.Context, id string, data []byte) (string, error) {
	if s.service.changes == nil {
		return "", errors.New("change reference resolver is unavailable")
	}
	data, err := s.service.changes.Canonicalize(data, true)
	if err != nil {
		return "", err
	}
	return s.put(ctx, record.Record{Version: 1, ID: id, Changes: data})
}

func outputName(namespace, id string) string {
	return fmt.Sprintf("output-%x.json", sha256.Sum256([]byte(namespace+"\x00"+id)))
}
func cursorName(namespace, digest string) string {
	return fmt.Sprintf("cursor-%x.json", sha256.Sum256([]byte(namespace+"\x00"+digest)))
}
func (s *Session) name(id string) (string, error) {
	owner, err := s.store.HandleOwner(s.scope.Namespace, id)
	return outputName(owner, id), err
}

func (s *Session) decode(data []byte, id string, strict bool) (record.Record, error) {
	var canonicalize func([]byte, bool) ([]byte, error)
	if s.service.changes != nil {
		canonicalize = s.service.changes.Canonicalize
	}
	return record.Decode(data, id, strict, canonicalize)
}

func (s *Session) write(tx *history.Transaction, name, pattern string, data []byte) error {
	if err := tx.Retain(s.scope.Thread, s.limits, name); err != nil {
		return err
	}
	if err := tx.Admit(s.limits, name, int64(len(data))); err != nil {
		return err
	}
	return tx.Write(name, pattern, data)
}

func (s *Session) put(ctx context.Context, value record.Record) (string, error) {
	if err := record.Validate(value); err != nil {
		return "", err
	}
	data, err := record.Encode(value)
	if err != nil {
		return "", err
	}
	if len(data) > history.MaxRecordBytes {
		return "", &history.CapacityError{Resource: "encoded read record", Required: int64(len(data)), Limit: history.MaxRecordBytes, Action: "Narrow the command output or split the operation before retrying."}
	}
	err = s.store.Transaction(ctx, s.writes, func(tx *history.Transaction) error {
		name, err := s.name(value.ID)
		if err != nil {
			return err
		}
		if previous, err := history.ReadRecord(filepath.Join(s.scope.Directory, name)); err == nil {
			if !bytes.Equal(previous, data) {
				return errors.New("read reference conflicts with retained record")
			}
			return s.retain(tx, value)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return s.write(tx, name, "output-pending-", data)
	})
	if err != nil {
		return "", err
	}
	return value.ID, nil
}

func (s *Session) read(ctx context.Context, id string) (record.Record, error) {
	var value record.Record
	if !record.ValidID(id) {
		return value, errors.New("invalid read reference")
	}
	err := s.store.Transaction(ctx, s.writes, func(tx *history.Transaction) error {
		name, err := s.name(id)
		if err != nil {
			return err
		}
		data, err := history.ReadRecord(filepath.Join(s.scope.Directory, name))
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read reference %s is unavailable; session data may have been cleaned after 14 days of inactivity or storage pressure", id)
		}
		if err != nil {
			return err
		}
		value, err = s.decode(data, id, true)
		if err != nil {
			return err
		}
		return s.retain(tx, value)
	})
	return value, err
}

// Snapshot returns the complete original payload under the caller's snapshot or
// session lease. It validates namespace ownership without advancing a cursor.
func (s *Session) Snapshot(id string) (command.Output, error) {
	name, err := s.name(id)
	if err != nil {
		return command.Output{}, err
	}
	data, err := history.ReadRecord(filepath.Join(s.scope.Directory, name))
	if err != nil {
		return command.Output{}, err
	}
	value, err := s.decode(data, id, true)
	if err != nil {
		return command.Output{}, err
	}
	if value.Source != "" || len(value.Changes) != 0 {
		return command.Output{}, errors.New("reload requires an original output snapshot")
	}
	return command.Output{Stdout: value.Stdout, Stderr: value.Stderr, ExitCode: value.ExitCode}, nil
}

func (s *Session) putCursor(ctx context.Context, source record.Record, binding string, position [2]int, stream string) (string, error) {
	value := record.Record{Version: 1, Source: source.ID, Position: position, Stream: stream, Binding: binding}
	value.CursorDigest = record.CursorDigest(value)
	err := s.store.Transaction(ctx, s.writes, func(tx *history.Transaction) error {
		scope, _, err := s.store.ReadScope(s.scope.Namespace)
		if err != nil {
			return err
		}
		candidates := append(slices.Clone(scope.Inherited), history.Range{Namespace: scope.Namespace, End: scope.Next})
		for _, candidate := range candidates {
			name := cursorName(candidate.Namespace, value.CursorDigest)
			data, err := history.ReadRecord(filepath.Join(s.scope.Directory, name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &value.ID); err != nil {
				return err
			}
			number, ok := history.ParseHandle(value.ID)
			if !ok {
				return errors.New("invalid read cursor handle")
			}
			if number >= candidate.End {
				continue
			}
			owner, err := s.store.HandleOwner(s.scope.Namespace, value.ID)
			if err != nil || owner != candidate.Namespace {
				return errors.New("read cursor scope mismatch")
			}
			return tx.Retain(s.scope.Thread, s.limits, name)
		}
		handles, err := tx.ReserveHandles(s.scope.Namespace, 1)
		if err != nil {
			return err
		}
		value.ID = handles[0]
		data, err := json.Marshal(value.ID)
		if err != nil {
			return err
		}
		return s.write(tx, cursorName(s.scope.Namespace, value.CursorDigest), "cursor-pending-", data)
	})
	if err != nil {
		return "", err
	}
	return s.put(ctx, value)
}

func (s *Session) streams(ctx context.Context, value record.Record) (command.OmittedOutput, error) {
	if len(value.Changes) == 0 {
		return command.OmittedOutput{Stdout: value.Stdout, Stderr: value.Stderr, StdoutKind: value.StdoutKind, StderrKind: value.StderrKind}, nil
	}
	var output command.OmittedOutput
	err := s.store.Transaction(ctx, s.writes, func(tx *history.Transaction) error {
		var err error
		output, err = s.service.changes.Resolve(ctx, reference.View{Scope: s.scope, Retain: func(names ...string) error { return tx.Retain(s.scope.Thread, s.limits, names...) }}, value.Changes)
		return err
	})
	return output, err
}

// Dependencies inspects a reference under the caller's publication boundary.
// Missing or out-of-scope textual handles establish no ownership claim.
func (s *Session) Dependencies(id string) ([]string, error) {
	if !record.ValidID(id) {
		return nil, nil
	}
	name, err := s.name(id)
	if err != nil {
		return nil, nil
	}
	data, err := history.ReadRecord(filepath.Join(s.scope.Directory, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value, err := s.decode(data, id, false)
	if err != nil {
		return nil, errors.New("invalid inherited read recovery record")
	}
	return s.dependencies(value)
}

func (s *Session) dependencies(value record.Record) ([]string, error) {
	var names []string
	seen := make(map[string]bool)
	for {
		if seen[value.ID] {
			return nil, errors.New("cyclic read recovery reference")
		}
		seen[value.ID] = true
		owner, err := s.store.HandleOwner(s.scope.Namespace, value.ID)
		if err != nil {
			return nil, err
		}
		if value.CursorDigest != "" {
			names = append(names, cursorName(owner, value.CursorDigest))
		}
		names = append(names, outputName(owner, value.ID))
		if len(value.Changes) > 0 {
			dependencies, err := s.service.changes.Dependencies(s.scope, value.Changes)
			if err != nil {
				return nil, err
			}
			names = append(names, dependencies...)
		}
		if value.Source == "" {
			return names, nil
		}
		id := value.Source
		name, err := s.name(id)
		if err != nil {
			return nil, err
		}
		data, err := history.ReadRecord(filepath.Join(s.scope.Directory, name))
		if err != nil {
			return nil, fmt.Errorf("read recovery source is unavailable: %w", err)
		}
		value, err = s.decode(data, id, false)
		if err != nil {
			return nil, errors.New("invalid read recovery source")
		}
	}
}
func (s *Session) retain(tx *history.Transaction, value record.Record) error {
	names, err := s.dependencies(value)
	if err != nil {
		return err
	}
	return tx.Retain(s.scope.Thread, s.limits, names...)
}

func RecordKinds() []recordkind.Spec {
	hashed := func(name, prefix string) bool {
		if filepath.Base(name) != name {
			return false
		}
		hash, ok := strings.CutPrefix(name, prefix)
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
	return []recordkind.Spec{
		{ID: "cursor", Match: func(name string) bool { return hashed(name, "cursor-") }},
		{ID: "output", Limit: 256 << 20, Match: func(name string) bool {
			if hashed(name, "output-") {
				return true
			}
			id, ok := strings.CutPrefix(name, "output-")
			if !ok {
				return false
			}
			id, ok = strings.CutSuffix(id, ".json")
			return ok && record.ValidID(id)
		}},
	}
}
