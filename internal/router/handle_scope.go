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

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func (s *mekugiReplayStore) handleNamespace() string {
	return s.session.Namespace
}

func (s *mekugiReplayStore) prepareHandleScope(ctx context.Context, metadata codexTurnMetadata) (context.Context, error) {
	if s == nil {
		return ctx, nil
	}
	identity, _ := ctx.Value(storageSessionKey{}).(storageSessionIdentity)
	err := s.locked(ctx, func(s *mekugiReplayStore) error {
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
			identity.Namespace, err = s.publication.NamespaceForThread(identity.Parent)
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
	if !identity.Initialize {
		return nil
	}
	return s.transaction.InitializeScope(history.Scope{Thread: identity.Thread, Namespace: identity.Namespace, Parent: identity.Parent, Fork: identity.Fork}, func(source string) error {
		return s.cloneHandleData(source, identity.Thread, identity.Fork, visibleFiles, releaseSnapshot)
	})
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
		if hashedRecordName(entry.Name(), changeIndexPrefix) {
			data, err := history.ReadRecord(filepath.Join(s.directory, entry.Name()))
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
		if !history.CatalogName(entry.Name()) {
			continue
		}
		catalog, err := s.publication.ReadCatalog(entry.Name())
		if err != nil {
			return err
		}
		scope, _, err := s.publication.ReadScope(catalog.Thread)
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
					if stream != index.streamName(i) {
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
	owner, err := s.publication.HandleOwner(s.handleNamespace(), id)
	return scopedOutputName(owner, id), err
}
