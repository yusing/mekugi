package history

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Source: internal/router/handle_scope.go:15:99@efcbf71f retained scope encoding and identity.
// Scope metadata outlives reclaimed payloads so resuming an expired session
// cannot reuse one of its old handles.
type Scope struct {
	Version          int
	Thread           string
	Namespace        string
	Parent           string `json:",omitzero"`
	Fork             string `json:",omitzero"`
	NextChangeStream int    `json:",omitzero"`
	Next             uint64
	Inherited        []Range `json:",omitzero"`
}

// A fork freezes the allocation ranges of its ancestors. Payloads are immutable
// and shared, but allocations after the snapshot belong only to the new branch.
type Range struct {
	Namespace string
	End       uint64
}

func ScopeName(thread string) string {
	return fmt.Sprintf("handle-scope-%x", sha256.Sum256([]byte(thread)))
}

func (s *Store) ReadScope(thread string) (Scope, bool, error) {
	scope := Scope{Version: 1, Thread: thread, Namespace: thread}
	name := ScopeName(thread)
	data, err := ReadRecord(filepath.Join(s.directory, name))
	if errors.Is(err, os.ErrNotExist) {
		return scope, false, nil
	}
	if err != nil {
		return scope, false, err
	}
	return decodeScope(data, thread)
}

// ScopeInNamespace filters catalog discovery before full scope validation.
// Unrelated feature metadata cannot affect this namespace's inherited records.
func (s *Store) ScopeInNamespace(thread, namespace string) (bool, error) {
	data, err := ReadRecord(filepath.Join(s.directory, ScopeName(thread)))
	if errors.Is(err, os.ErrNotExist) {
		return thread == namespace, nil
	}
	if err != nil {
		return false, err
	}
	var identity struct {
		Version           int
		Thread, Namespace string
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return false, fmt.Errorf("decode handle scope identity: %w", err)
	}
	if identity.Version != 1 || identity.Thread != thread || identity.Namespace == "" {
		return false, errors.New("invalid handle scope identity")
	}
	if identity.Namespace != namespace {
		return false, nil
	}
	_, _, err = decodeScope(data, thread)
	return err == nil, err
}

func decodeScope(data []byte, thread string) (Scope, bool, error) {
	var scope Scope
	if err := json.Unmarshal(data, &scope, json.RejectUnknownMembers(true)); err != nil {
		return scope, false, fmt.Errorf("decode handle scope: %w", err)
	}
	if scope.NextChangeStream < 0 || scope.Version != 1 || scope.Thread != thread || thread != "" && scope.Namespace == "" ||
		scope.Parent != "" && scope.Parent == thread || scope.Fork != "" && scope.Fork == thread ||
		scope.Namespace != thread && (scope.Parent == "" || scope.Next != 0 || scope.NextChangeStream != 0 || len(scope.Inherited) != 0 || scope.Fork != "") {
		return scope, false, errors.New("invalid handle scope identity")
	}
	var end uint64
	seen := map[string]bool{thread: true}
	for _, ancestor := range scope.Inherited {
		if seen[ancestor.Namespace] || ancestor.End <= end || ancestor.End > scope.Next {
			return scope, false, errors.New("invalid inherited handle range")
		}
		seen[ancestor.Namespace], end = true, ancestor.End
	}
	return scope, true, nil
}

func (tx *Transaction) WriteScope(scope Scope) error {
	if err := tx.check(); err != nil {
		return err
	}
	data, err := json.Marshal(&scope)
	if err != nil {
		return err
	}
	if len(data) > MaxRecordBytes {
		return errors.New("handle scope exceeds capacity")
	}
	return tx.Write(ScopeName(scope.Thread), "handle-scope-pending-", data)
}

// Called under store.lock. Durable thread bindings, not routing IDs or the live
// activity tree, also let a standalone shell worker restore a child's scope.
func (s *Store) NamespaceForThread(thread string) (string, error) {
	scope, found, err := s.ReadScope(thread)
	if err == nil && found && scope.Namespace != thread {
		root, exists, readErr := s.ReadScope(scope.Namespace)
		if readErr != nil {
			return "", readErr
		}
		if !exists || root.Namespace != scope.Namespace {
			return "", errors.New("missing or inconsistent root handle scope")
		}
	}
	return scope.Namespace, err
}

// InitializeScope publishes durable ancestry only after the operation's inherited
// feature records are available. A failed inheritance callback remains retryable.
// Source: internal/router/handle_scope.go@b8ca48c7 namespace reconciliation.
func (tx *Transaction) InitializeScope(identity Scope, inherit func(source string) error) error {
	if err := tx.check(); err != nil {
		return err
	}
	if identity.Thread == "" {
		return nil
	}
	prior, exists, err := tx.store.ReadScope(identity.Thread)
	if err != nil {
		return err
	}
	if exists {
		if prior.Namespace != identity.Namespace || identity.Parent != "" && prior.Parent != identity.Parent || identity.Fork != "" && prior.Fork != identity.Fork {
			return errors.New("thread handle scope conflict")
		}
		return nil
	}
	if identity.Parent != "" {
		namespace, err := tx.store.NamespaceForThread(identity.Parent)
		if err != nil {
			return err
		}
		if namespace != identity.Namespace {
			return errors.New("parent handle scope changed during request preparation; retry the request")
		}
		if err := tx.initializeRootScope(identity.Namespace, "", nil); err != nil {
			return err
		}
		return tx.WriteScope(Scope{Version: 1, Thread: identity.Thread, Namespace: identity.Namespace, Parent: identity.Parent})
	}
	return tx.initializeRootScope(identity.Thread, identity.Fork, inherit)
}

func (tx *Transaction) initializeRootScope(thread, fork string, inherit func(source string) error) error {
	if scope, exists, err := tx.store.ReadScope(thread); err != nil || exists {
		if err == nil && scope.Namespace != thread {
			return errors.New("subagent cannot become a root handle scope")
		}
		return err
	}
	if fork == thread {
		return errors.New("handle scope fork cannot refer to itself")
	}
	dest := Scope{Version: 1, Thread: thread, Namespace: thread, Fork: fork}
	if fork != "" {
		source, err := tx.store.NamespaceForThread(fork)
		if err != nil {
			return err
		}
		if err := tx.initializeRootScope(source, "", nil); err != nil {
			return err
		}
		prior, _, err := tx.store.ReadScope(source)
		if err != nil {
			return err
		}
		dest.Next, dest.Inherited = prior.Next, slices.Clone(prior.Inherited)
		dest.NextChangeStream = prior.NextChangeStream
		if len(dest.Inherited) == 0 && prior.Next > 0 || len(dest.Inherited) > 0 && dest.Inherited[len(dest.Inherited)-1].End < prior.Next {
			dest.Inherited = append(dest.Inherited, Range{Namespace: source, End: prior.Next})
		}
		if inherit != nil {
			if err := inherit(source); err != nil {
				return err
			}
		}
	}
	return tx.WriteScope(dest)
}

func (s *Store) HandleOwner(namespace, id string) (string, error) {
	scope, _, err := s.ReadScope(namespace)
	if err != nil {
		return "", err
	}
	number, valid := ParseHandle(id)
	if !valid || number >= scope.Next {
		return "", fmt.Errorf("handle %s is unavailable in this session", id)
	}
	for _, ancestor := range scope.Inherited {
		if number < ancestor.End {
			return ancestor.Namespace, nil
		}
	}
	return namespace, nil
}

// ReserveStream retains the namespace-wide high-water mark. The feature owner
// supplies the floor discovered from historical records and formats the result.
func (tx *Transaction) ReserveStream(namespace string, floor int) (int, error) {
	if err := tx.check(); err != nil {
		return 0, err
	}
	scope, _, err := tx.store.ReadScope(namespace)
	if err != nil {
		return 0, err
	}
	if scope.Namespace != namespace {
		return 0, errors.New("change stream allocation requires the root namespace")
	}
	next := max(scope.NextChangeStream, floor)
	if next == int(^uint(0)>>1) {
		return 0, errors.New("change stream counter exhausted")
	}
	scope.NextChangeStream = next + 1
	return next, tx.WriteScope(scope)
}
