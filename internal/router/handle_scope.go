package router

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Scope metadata outlives reclaimed payloads so resuming an expired session
// cannot reuse one of its old handles.
type handleScope struct {
	Version   int
	Thread    string
	Namespace string
	Parent    string `json:",omitzero"`
	Fork      string `json:",omitzero"`
	Next      uint64
	Inherited []handleRange `json:",omitzero"`
}

// A fork freezes the allocation ranges of its ancestors. Payloads are immutable
// and shared, but allocations after the snapshot belong only to the new branch.
type handleRange struct {
	Namespace string
	End       uint64
}

func handleScopeName(thread string) string {
	return fmt.Sprintf("handle-scope-%x", sha256.Sum256([]byte(thread)))
}

func (s *mekugiReplayStore) handleNamespace() string {
	return s.session.Namespace
}

func (s *mekugiReplayStore) readHandleScope(thread string) (handleScope, bool, error) {
	scope := handleScope{Version: 1, Thread: thread, Namespace: thread}
	name := handleScopeName(thread)
	data, err := readManagedOutputFile(filepath.Join(s.directory, name))
	if errors.Is(err, os.ErrNotExist) {
		return scope, false, nil
	}
	if err != nil {
		return scope, false, err
	}
	scope = handleScope{}
	if err := json.Unmarshal(data, &scope, json.RejectUnknownMembers(true)); err != nil {
		return scope, false, fmt.Errorf("decode handle scope: %w", err)
	}
	if scope.Version != 1 || scope.Thread != thread || thread != "" && scope.Namespace == "" ||
		scope.Parent != "" && scope.Parent == thread || scope.Fork != "" && scope.Fork == thread ||
		scope.Namespace != thread && (scope.Parent == "" || scope.Next != 0 || len(scope.Inherited) != 0 || scope.Fork != "") {
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

func (s *mekugiReplayStore) writeHandleScope(scope handleScope) error {
	data, err := json.Marshal(&scope)
	if err != nil {
		return err
	}
	if len(data) > maxReplayRecordBytes {
		return errors.New("handle scope exceeds capacity")
	}
	return storageIOError(s.writeFile(handleScopeName(scope.Thread), "handle-scope-pending-", data))
}

// Called under store.lock. Durable thread bindings, not routing IDs or the live
// activity tree, also let a standalone shell worker restore a child's scope.
func (s *mekugiReplayStore) namespaceForThread(thread string) (string, error) {
	scope, found, err := s.readHandleScope(thread)
	if err == nil && found && scope.Namespace != thread {
		root, exists, readErr := s.readHandleScope(scope.Namespace)
		if readErr != nil {
			return "", readErr
		}
		if !exists || root.Namespace != scope.Namespace {
			return "", errors.New("missing or inconsistent root handle scope")
		}
	}
	return scope.Namespace, err
}

func (s *mekugiReplayStore) prepareHandleScope(ctx context.Context, metadata codexTurnMetadata) (context.Context, error) {
	if s == nil {
		return ctx, nil
	}
	identity, _ := ctx.Value(storageSessionKey{}).(storageSessionIdentity)
	err := s.locked(ctx, func() error {
		if metadata.ThreadID != "" && metadata.ThreadID != identity.Thread ||
			metadata.SubagentKind != "" && metadata.ParentThreadID == "" {
			return nil // Invalid auxiliary ancestry cannot change a retained binding.
		}
		// Agent names control presentation, not session identity. A malformed
		// name must not discard an otherwise valid stable parent relationship.
		identity.Initialize = true
		if metadata.SubagentKind != "" && metadata.ParentThreadID != "" {
			identity.Parent = metadata.ParentThreadID
			if identity.Parent == identity.Thread {
				return errors.New("handle scope parent cannot refer to itself")
			}
			var err error
			identity.Namespace, err = s.namespaceForThread(identity.Parent)
			return err
		}
		if metadata.SubagentKind == "" {
			identity.Fork = metadata.ForkedFromThreadID
		}
		return nil
	})
	return context.WithValue(ctx, storageSessionKey{}, identity), err
}

// Invoked only after replay validation, before retaining/confirming inherited
// references. Publishing the binding last makes an interrupted clone retryable.
func (s *mekugiReplayStore) initializeHandleScope(visibleFiles []string, releaseSnapshot func()) error {
	identity := s.session
	if !identity.Initialize || identity.Thread == "" {
		return nil
	}
	prior, exists, err := s.readHandleScope(identity.Thread)
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
		namespace, err := s.namespaceForThread(identity.Parent)
		if err != nil {
			return err
		}
		if namespace != identity.Namespace {
			return errors.New("parent handle scope changed during request preparation; retry the request")
		}
		if err := s.initializeRootHandleScope(identity.Namespace, "", nil, nil); err != nil {
			return err
		}
		return s.writeHandleScope(handleScope{Version: 1, Thread: identity.Thread, Namespace: identity.Namespace, Parent: identity.Parent})
	}
	return s.initializeRootHandleScope(identity.Thread, identity.Fork, visibleFiles, releaseSnapshot)
}

func (s *mekugiReplayStore) initializeRootHandleScope(thread, fork string, visibleFiles []string, releaseSnapshot func()) error {
	if scope, exists, err := s.readHandleScope(thread); err != nil || exists {
		if err == nil && scope.Namespace != thread {
			return errors.New("subagent cannot become a root handle scope")
		}
		return err
	}
	if fork == thread {
		return errors.New("handle scope fork cannot refer to itself")
	}
	dest := handleScope{Version: 1, Thread: thread, Namespace: thread, Fork: fork}
	if fork != "" {
		source, err := s.namespaceForThread(fork)
		if err != nil {
			return err
		}
		if err := s.initializeRootHandleScope(source, "", nil, nil); err != nil {
			return err
		}
		prior, _, err := s.readHandleScope(source)
		if err != nil {
			return err
		}
		dest.Next, dest.Inherited = prior.Next, slices.Clone(prior.Inherited)
		if len(dest.Inherited) == 0 && prior.Next > 0 || len(dest.Inherited) > 0 && dest.Inherited[len(dest.Inherited)-1].End < prior.Next {
			dest.Inherited = append(dest.Inherited, handleRange{Namespace: source, End: prior.Next})
		}
		if err := s.cloneHandleData(source, thread, fork, visibleFiles, releaseSnapshot); err != nil {
			return err
		}
	}
	return s.writeHandleScope(dest)
}

func (s *mekugiReplayStore) handleOwner(id string) (string, error) {
	namespace := s.handleNamespace()
	scope, _, err := s.readHandleScope(namespace)
	if err != nil {
		return "", err
	}
	number, valid := parseShortHandle(id)
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

func (s *mekugiReplayStore) cloneHandleData(source, dest, fork string, visibleFiles []string, releaseSnapshot func()) error {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	files := make(map[string]bool)
	for _, file := range visibleFiles {
		files[file] = true
	}
	var indexes []changeIndex
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), changeIndexPrefix) && retainedDataName(entry.Name()) {
			data, err := readManagedOutputFile(filepath.Join(s.directory, entry.Name()))
			if err != nil {
				return err
			}
			var index changeIndex
			if err := json.Unmarshal(data, &index); err != nil {
				return err
			}
			if index.Namespace != source {
				continue
			}
			if index.Version != 1 || changeIndexName(index.Workspace, source) != entry.Name() || index.Changes == nil {
				return errors.New("invalid cloned change index identity")
			}
			if err := validateChangeIndex(index); err != nil {
				return err
			}
			indexes = append(indexes, index)
			for _, change := range index.Changes {
				for _, call := range change.Calls {
					files[replayRecordName(index.Workspace, call.ID, false)] = true
				}
			}
			continue
		}
		if !storageCatalogName(entry.Name()) {
			continue
		}
		catalog, err := s.readRetainedSession(entry.Name())
		if err != nil {
			return err
		}
		scope, _, err := s.readHandleScope(catalog.Thread)
		if err != nil {
			return err
		}
		if scope.Namespace != source {
			continue
		}
		maps.Copy(files, catalog.Files)
	}
	// Pin dependencies before any cloned index write can trigger cleanup.
	if releaseSnapshot != nil {
		// store.lock now protects the validated facts. Release the shared
		// snapshot lease before our own quota check may need exclusive cleanup.
		releaseSnapshot()
	}
	if err := s.retainFiles(slices.Collect(maps.Keys(files))...); err != nil {
		return err
	}
	for _, index := range indexes {
		index.Namespace = dest
		for i := range index.Streams {
			if index.Streams[i].Thread == fork {
				// Preserve the original author when the cloned stream changes owner.
				for id, change := range index.Changes {
					stream, _, _ := parseChangeID(id)
					if stream != changeStreamName(i) {
						continue
					}
					for j := range change.Calls {
						if change.Calls[j].Thread == "" {
							change.Calls[j].Thread = fork
						}
					}
					index.Changes[id] = change
				}
				index.Streams[i].Thread = dest
			}
		}
		if err := s.writeChangeIndex(index); err != nil {
			return err
		}
	}
	return nil
}

func scopedOutputName(namespace, id string) string {
	return fmt.Sprintf("output-%x.json", sha256.Sum256([]byte(namespace+"\x00"+id)))
}

func scopedCursorName(namespace, digest string) string {
	return fmt.Sprintf("cursor-%x.json", sha256.Sum256([]byte(namespace+"\x00"+digest)))
}

func (s *mekugiReplayStore) outputName(id string) (string, error) {
	owner, err := s.handleOwner(id)
	return scopedOutputName(owner, id), err
}

func (s *mekugiReplayStore) hpatchCallID(id string) (string, error) {
	owner, err := s.handleOwner(id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("hpatch-%x-%s", sha256.Sum256([]byte(owner)), id), nil
}
