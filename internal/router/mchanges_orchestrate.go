package router

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

type changeReadScope struct {
	workspace string
	namespace string
	identity  string
	index     changeIndex
	ids       []string
}

// Explicit coordinator reads can cross checkout boundaries. The journal locates
// the run, and its manifest plus the child's independent journal prove membership.
// Neither a workspace operand nor a live roster grants access to another namespace.
// Called under store.lock; Snapshot does not acquire the orchestration run lock.
func (s *mekugiReplayStore) coordinatorChangeScopes(options changeReadOptions) ([]changeReadScope, error) {
	if s.session.Thread == "" {
		return nil, nil
	}
	scope, _, err := s.readHandleScope(s.session.Thread)
	if err != nil {
		return nil, err
	}
	run := scope.Coordinator
	if run == nil {
		// Older bindings need discovery. A durable locator avoids reading
		// unrelated journals; every path still validates the exact roots.
		workspaces, err := (&journalStore{}).journalWorkspaces(s)
		if err != nil {
			return nil, err
		}
		for _, view := range workspaces {
			for _, candidate := range view.journals {
				reference := candidate.Orchestration
				if reference == nil || reference.Main != s.session.Thread {
					continue
				}
				if run != nil && *run != *reference {
					return nil, errors.New("coordinator workspace is ambiguous")
				}
				run = reference
			}
		}
	}
	if run == nil {
		return nil, nil
	}
	read := func(workspace, thread string) (threadJournal, bool, error) {
		return readThreadJournal(s, workspace, thread)
	}
	if options.workspaceSet && options.workspace == run.Workspace {
		return nil, run.validateCoordinator(read)
	}
	members, err := run.members(read)
	if err != nil {
		return nil, err
	}
	var scopes []changeReadScope
	var failures []error
	for _, member := range members {
		child := member.journal
		if options.workspaceSet && options.workspace != member.batch.Cwd {
			continue
		}
		if member.err != nil {
			failures = append(failures, fmt.Errorf("workspace %s: %w", member.batch.Cwd, member.err))
			continue
		}
		namespace, err := s.namespaceForThread(child.Thread)
		if err == nil && namespace != child.Thread {
			err = errors.New("orchestration child is not an independent change namespace")
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("workspace %s: %w", child.Workspace, err))
			continue
		}
		scopes = append(scopes, changeReadScope{workspace: child.Workspace, namespace: namespace, identity: journalFilename(child.Workspace, child.Thread)})
	}
	return scopes, errors.Join(failures...)
}

// Allocation ownership includes retired IDs, whose payload is absent.
func (index changeIndex) ownsChange(id string) bool {
	name, number, _ := parseChangeID(id)
	for position, stream := range index.Streams {
		if index.streamName(position) == name && number <= stream.Next {
			return true
		}
	}
	return false
}

func (s *mekugiReplayStore) readSelectedChanges(ctx context.Context, options changeReadOptions) (string, *changeReadSnapshot, error) {
	s = s.scoped(ctx)
	var output strings.Builder
	var parts []*changeReadSnapshot
	var failures []error
	err := s.locked(ctx, func() error {
		scopes := []changeReadScope{{workspace: options.workspace, namespace: s.handleNamespace()}}
		index, err := s.readChangeIndex(options.workspace)
		if err != nil {
			failures = append(failures, fmt.Errorf("workspace %s: %w", options.workspace, err))
		} else {
			scopes[0].index = index
		}
		var unresolved []string
		for _, id := range options.ids {
			if options.workspaceSet && scopes[0].index.ownsChange(id) {
				scopes[0].ids = append(scopes[0].ids, id)
			} else {
				unresolved = append(unresolved, id)
			}
		}
		if len(unresolved) != 0 {
			children, err := s.coordinatorChangeScopes(options)
			if err != nil {
				failures = append(failures, err)
			}
			for _, child := range children {
				reader := *s
				reader.session.Namespace = child.namespace
				index, err := reader.readChangeIndex(child.workspace)
				if err != nil {
					failures = append(failures, fmt.Errorf("workspace %s: %w", child.workspace, err))
					continue
				}
				child.index = index
				scopes = append(scopes, child)
			}
		}
		for _, id := range unresolved {
			stream, number, _ := parseChangeID(id)
			var owners, streams []int
			var workspaces []string
			for i, scope := range scopes {
				for position := range scope.index.Streams {
					if scope.index.streamName(position) == stream {
						streams = append(streams, i)
						if number <= scope.index.Streams[position].Next {
							owners = append(owners, i)
							workspaces = append(workspaces, scope.workspace)
						}
						break
					}
				}
			}
			if len(owners) == 0 && len(streams) == 1 {
				owners = streams // Preserve the owning stream's never-allocated diagnostic.
			}
			switch len(owners) {
			case 0:
				scopes[0].ids = append(scopes[0].ids, id)
			case 1:
				scopes[owners[0]].ids = append(scopes[owners[0]].ids, id)
			default:
				failures = append(failures, fmt.Errorf("change %s is ambiguous across workspaces %s; select --workspace DIR", id, strings.Join(workspaces, ", ")))
			}
		}
		var names []string
		for _, scope := range scopes {
			if len(scope.ids) != 0 && scope.identity != "" {
				names = append(names, scope.identity)
			}
		}
		if len(names) != 0 {
			if err := s.retainFiles(names...); err != nil {
				return err
			}
		}
		for _, scope := range scopes {
			if len(scope.ids) == 0 || scope.index.Version == 0 {
				continue
			}
			selection := options
			selection.workspace, selection.ids = scope.workspace, scope.ids
			reader := *s
			reader.session.Namespace = scope.namespace
			snapshot, err := reader.selectChangeSnapshot(selection, scope.index)
			if err != nil {
				if len(scopes) != 1 || len(failures) != 0 {
					err = fmt.Errorf("workspace %s: %w", scope.workspace, err)
				}
				failures = append(failures, err)
			}
			if snapshot == nil || len(snapshot.IDs) == 0 && snapshot.Available == "" {
				continue
			}
			// Composition belongs to the workspace, not the ID namespace.
			// Ownership resolution has already rejected ambiguous IDs, and
			// each source index and attempt is retained before merging.
			if (options.view == "net" || options.view == "summary") && snapshot.Available == "" {
				if i := slices.IndexFunc(parts, func(part *changeReadSnapshot) bool {
					return part.Workspace == snapshot.Workspace && part.Available == ""
				}); i >= 0 {
					parts[i].IDs = append(parts[i].IDs, snapshot.IDs...)
					maps.Copy(parts[i].Selected, snapshot.Selected)
					continue
				}
			}
			parts = append(parts, snapshot)
		}
		var rendered []*changeReadSnapshot
		for _, part := range parts {
			text := part.Available
			if text == "" {
				selection := options
				selection.workspace, selection.ids, selection.overlapLabels = part.Workspace, part.IDs, part.OverlapLabels
				text, err = s.renderChanges(ctx, selection, changeIndex{Workspace: part.Workspace, Changes: part.Selected, Streams: part.Streams})
				if err != nil {
					failures = append(failures, fmt.Errorf("workspace %s: %w", part.Workspace, err))
				}
			}
			if output.Len()+len(text) > maxChangeReadBytes {
				failures = append(failures, errors.New("change read exceeds 64 MiB; narrow the selection"))
				break
			}
			output.WriteString(text)
			if text != "" {
				rendered = append(rendered, part)
			}
		}
		parts = rendered
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	var readErr error
	if len(failures) != 0 {
		readErr = &partialChangeReadError{errors.Join(failures...)}
	}
	if len(parts) == 1 {
		return output.String(), parts[0], readErr
	}
	return output.String(), &changeReadSnapshot{Frozen: true, Parts: parts}, readErr
}

// Coordinator access is read-only. Do not mistake a child's ID for a colliding
// local ID and apply it to Main's checkout. An explicit local workspace keeps
// ordinary caller-namespace mutation semantics.
func (s *mekugiReplayStore) checkCoordinatorChangeMutation(options changeReadOptions, callerIndex changeIndex) error {
	if options.workspaceSet && !slices.ContainsFunc(options.ids, func(id string) bool { return !callerIndex.ownsChange(id) }) {
		return nil // Retained caller ownership does not depend on run storage.
	}
	children, err := s.coordinatorChangeScopes(options)
	if err != nil {
		return err
	}
	for _, child := range children {
		reader := *s
		reader.session.Namespace = child.namespace
		index, err := reader.readChangeIndex(child.workspace)
		if err != nil {
			return fmt.Errorf("workspace %s: %w", child.workspace, err)
		}
		for _, id := range options.ids {
			if index.ownsChange(id) {
				return fmt.Errorf("change %s has child evidence in %s; coordinator access is read-only; select --workspace DIR for a caller-owned mutation", id, child.workspace)
			}
		}
	}
	return nil
}
