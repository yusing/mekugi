package router

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/orchestrate"
)

// A reference locates authority; only the run manifest grants membership.
type journalRun struct {
	Directory string `json:"directory"`
	Workspace string `json:"workspace"`
	Main      string `json:"main"`
}

// Called under the mutation's journal/replay locks. Complete run ancestry scopes
// the reports; the caller publishes them only after successful persistence.
func (s *journalStore) scopeOrchestratedBlocks(store *mekugiReplayStore, workspace, thread string, blocks []orchestrateEvent) []orchestrateEvent {
	if len(blocks) == 0 {
		return nil
	}
	journals, failures, err := s.relatedJournals(store, workspace, thread)
	if err != nil {
		return nil
	}
	chain := journalAncestry(journals, thread)
	if len(chain) < 2 {
		return nil
	}
	for _, id := range chain {
		if failures[id] != nil {
			return nil
		}
	}
	main, child := journals[chain[len(chain)-1]], journals[chain[len(chain)-2]]
	run := main.Orchestration
	if run == nil || run.Main != main.Thread || run.Workspace != main.Workspace || child.Orchestration == nil || *child.Orchestration != *run {
		return nil
	}
	for i := range blocks {
		blocks[i].main, blocks[i].workspace, blocks[i].directory = run.Main, run.Workspace, run.Directory
		blocks[i].Task = strings.TrimPrefix(child.Author, "/root/")
	}
	return blocks
}

func (s *journalStore) bindRun(ctx context.Context, store *mekugiReplayStore, workspace, thread string, run journalRun) error {
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || !j.IdentityKnown || j.IdentityConflicted || j.Parent != "" || j.Author != "/root" {
			return errors.New("orchestration journal requires a proven independent root")
		}
		if j.Orchestration != nil {
			if *j.Orchestration != run {
				return errors.New("journal orchestration reference changed")
			}
			return errJournalUnchanged
		}
		j.Orchestration = &run
		return nil
	})
}

// Called under journal/replay locks. Manifest reads are atomic snapshots, with
// no run-lock wait or checkout command. The stored journals remain independent.
func (s *journalStore) relatedJournals(store *mekugiReplayStore, workspace, caller string) (map[string]threadJournal, map[string]error, error) {
	snapshot, err := s.journalWorkspaces(store)
	if err != nil {
		return nil, nil, err
	}
	local, failures := snapshot[workspace].journals, snapshot[workspace].failures
	chain := journalAncestry(local, caller)
	if len(chain) == 0 {
		return local, failures, nil
	}
	root := local[chain[len(chain)-1]]
	run := root.Orchestration
	if run == nil {
		return local, failures, nil
	}
	batches, err := (&orchestrate.Store{Directory: run.Directory}).Snapshot(run.Workspace, run.Main)
	if err != nil {
		return local, failures, err
	}
	member := root.Thread == run.Main && root.Workspace == run.Workspace
	for _, b := range batches {
		if b.Launch != nil && b.Launch.ThreadID == root.Thread && b.Cwd == root.Workspace {
			member = true
		}
	}
	if !member {
		return local, failures, errors.New("journal root is not a confirmed member of its orchestration run")
	}
	mainJournals, mainErrors := snapshot[run.Workspace].journals, snapshot[run.Workspace].failures
	main, ok := mainJournals[run.Main]
	if !ok || mainErrors[run.Main] != nil || !main.IdentityKnown || main.IdentityConflicted || main.Parent != "" || main.Author != "/root" || main.Orchestration == nil || *main.Orchestration != *run {
		return local, failures, errors.New("orchestration coordinator journal authority is unavailable")
	}
	joined, errorsByThread := maps.Clone(mainJournals), maps.Clone(mainErrors)
	for _, b := range batches {
		if b.Launch == nil || b.Launch.ThreadID == "" {
			continue
		}
		journals, recordErrors := snapshot[b.Cwd].journals, snapshot[b.Cwd].failures
		child, exists := journals[b.Launch.ThreadID]
		if !exists || !child.IdentityKnown || child.IdentityConflicted || child.Parent != "" || child.Author != "/root" || child.Orchestration == nil || *child.Orchestration != *run {
			continue // A binding remains unresolved until both durable owners agree.
		}
		prefix := "/root/" + b.TaskName
		for thread, j := range journals {
			if !slices.Contains(journalAncestry(journals, thread), child.Thread) {
				continue
			}
			if _, exists := joined[thread]; exists {
				return local, failures, fmt.Errorf("orchestration journal thread %q has conflicting workspace ownership", thread)
			}
			rename := func(path string) string {
				if path == "/root" || strings.HasPrefix(path, "/root/") {
					return prefix + strings.TrimPrefix(path, "/root")
				}
				return path
			}
			j.Author = rename(j.Author)
			roles := make(map[string]journalSpawnRole, len(j.SpawnRoles))
			for path, role := range j.SpawnRoles {
				roles[rename(path)] = role
			}
			j.SpawnRoles = roles
			j.Items = slices.Clone(j.Items)
			for i := range j.Items {
				j.Items[i].Author, j.Items[i].Agent = rename(j.Items[i].Author), rename(j.Items[i].Agent)
			}
			if thread == child.Thread {
				j.Parent = run.Main
			}
			joined[thread], errorsByThread[thread] = j, recordErrors[thread]
		}
	}
	if store != nil && store.session.Thread != "" {
		var names []string
		for thread, j := range joined {
			if j.Workspace != workspace && journalRelative(joined, caller, thread) {
				names = append(names, journalFilename(j.Workspace, thread))
			}
		}
		if len(names) > 0 {
			if err := store.retainFiles(names...); err != nil {
				return local, failures, err
			}
		}
	}
	return joined, errorsByThread, nil
}
