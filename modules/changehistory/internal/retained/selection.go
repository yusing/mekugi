package retained

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	json "encoding/json/v2"

	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func (index changeIndex) workspaceFor(id string) string {
	stream, _, _ := changes.ParseID(id)
	if workspace, ok := index.workspaces[stream]; ok {
		return workspace
	}
	return index.Workspace
}

func (index changeIndex) indexNames(ids []string) []string {
	if index.workspaces == nil {
		return []string{changeIndexName(index.Workspace, index.Namespace)}
	}
	var names []string
	for _, root := range index.authoredWorkspaces(ids) {
		name := changeIndexName(root, index.Namespace)
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func (index changeIndex) authoredWorkspaces(ids []string) []string {
	var roots []string
	for _, id := range ids {
		if index.workspaces != nil {
			stream, _, _ := changes.ParseID(id)
			if _, exists := index.workspaces[stream]; !exists {
				continue
			}
		}
		root := index.workspaceFor(id)
		if !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	return roots
}

// Select ownership from durable session evidence, never the shell's directory.
// The merged index is a read projection, not another published index.
func (s *store) selectChangeIndex(options *changeReadOptions) (changeIndex, error) {
	if options.cwd != "" {
		if directory, err := filepath.EvalSymlinks(options.cwd); err == nil {
			options.cwd, options.workspace = directory, directory
		}
	}
	index := changeIndex{Version: 1, Workspace: options.workspace, Namespace: s.handleNamespace(), Changes: make(map[string]trackedChange), workspaces: make(map[string]string)}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return index, err
	}
	ambiguous := make(map[string]bool)
	for _, entry := range entries {
		if !hashedRecordName(entry.Name(), changeIndexPrefix) {
			continue
		}
		data, err := history.ReadRecord(filepath.Join(s.directory, entry.Name()))
		if err != nil {
			index.discoveryErr = errors.Join(index.discoveryErr, fmt.Errorf("change index %s: %w", entry.Name(), err))
			continue
		}
		var identity struct{ Namespace string }
		if err := json.Unmarshal(data, &identity); err != nil {
			index.discoveryErr = errors.Join(index.discoveryErr, fmt.Errorf("change index %s: %w", entry.Name(), err))
			continue
		}
		if identity.Namespace != index.Namespace {
			continue
		}
		var authored changeIndex
		if err := json.Unmarshal(data, &authored); err != nil {
			index.discoveryErr = errors.Join(index.discoveryErr, fmt.Errorf("change index %s: %w", entry.Name(), err))
			continue
		}
		if authored.Version != 1 || authored.Changes == nil || changeIndexName(authored.Workspace, authored.Namespace) != entry.Name() {
			index.discoveryErr = errors.Join(index.discoveryErr, fmt.Errorf("change index %s: invalid identity", entry.Name()))
			continue
		}
		if err := validateChangeIndex(authored); err != nil {
			index.discoveryErr = errors.Join(index.discoveryErr, fmt.Errorf("change index %s: %w", entry.Name(), err))
			continue
		}
		for position, stream := range authored.Streams {
			name := authored.streamName(position)
			if ambiguous[name] {
				continue
			}
			if previous, exists := index.workspaces[name]; exists {
				index.discoveryErr = errors.Join(index.discoveryErr, fmt.Errorf("change stream %s has ambiguous authored workspaces %q and %q", name, previous, authored.Workspace))
				ambiguous[name] = true
				delete(index.workspaces, name)
				index.Streams = slices.DeleteFunc(index.Streams, func(stream changeStream) bool { return stream.Name == name })
				maps.DeleteFunc(index.Changes, func(id string, _ trackedChange) bool {
					stream, _, _ := changes.ParseID(id)
					return stream == name
				})
				continue
			}
			stream.Name = name
			index.Streams = append(index.Streams, stream)
			index.workspaces[name] = authored.Workspace
		}
		for id, change := range authored.Changes {
			stream, _, _ := changes.ParseID(id)
			if !ambiguous[stream] {
				index.Changes[id] = change
			}
		}
	}
	slices.SortFunc(index.Streams, func(a, b changeStream) int {
		aOrdinal, _ := changeStreamOrdinal(a.Name)
		bOrdinal, _ := changeStreamOrdinal(b.Name)
		return aOrdinal - bOrdinal
	})
	if options.mine {
		if s.session.Thread == "" {
			return index, errors.New("own-thread reads require a Codex thread identity")
		}
		options.ids, err = threadChangeIDs(index, s.session.Thread)
		if err != nil {
			return index, err
		}
	}
	// Preserve authored-relative output for a single workspace, regardless of cwd.
	if roots := index.authoredWorkspaces(options.ids); len(roots) == 1 {
		options.workspace = roots[0]
	}
	return index, nil
}

// Resolve lexical filters against captured names. Existing files and symlinks
// cannot change a retained read. Authored roots take precedence over cwd.
func (s *store) resolveChangePaths(options *changeReadOptions, index changeIndex) error {
	if len(options.paths) == 0 {
		return nil
	}
	if !slices.ContainsFunc(options.paths, func(path string) bool { return !filepath.IsAbs(path) }) {
		for _, path := range options.paths {
			options.resolvedPaths = append(options.resolvedPaths, filepath.Clean(path))
		}
		return nil
	}
	known := make(map[string]bool)
changes:
	for _, id := range options.ids {
		paths := make(map[string]bool)
		for _, call := range index.Changes[id].Calls {
			record, err := s.readChangeRecord(index.workspaceFor(id), id, index.Changes[id].Correlation, call.ID)
			if err != nil {
				if options.unavailable.skip(id, err) {
					continue changes
				}
				return err
			}
			for _, file := range record.History.ReviewFiles {
				for _, path := range []string{file.BeforePath, file.AfterPath} {
					if path != "" {
						if !filepath.IsAbs(path) {
							path = filepath.Join(index.workspaceFor(id), path)
						}
						paths[filepath.Clean(path)] = true
					}
				}
			}
		}
		maps.Copy(known, paths)
	}
	roots := index.authoredWorkspaces(options.ids)
	var resolved []string
	for _, path := range options.paths {
		if filepath.IsAbs(path) {
			resolved = append(resolved, filepath.Clean(path))
			continue
		}
		matched := false
		for _, root := range roots {
			candidate := filepath.Join(root, path)
			if known[candidate] {
				resolved = append(resolved, candidate)
				matched = true
			}
		}
		if !matched {
			base := options.cwd
			if base == "" {
				base = options.workspace
			}
			resolved = append(resolved, filepath.Join(base, path))
		}
	}
	options.resolvedPaths = resolved
	return nil
}
