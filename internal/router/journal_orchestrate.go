package router

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
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

type journalRunMember struct {
	batch   orchestrate.Batch
	journal threadJournal
	err     error
}

// Admission uses current owned tasks, just as journal continuation does. Mounted
// native descendants and historical blocked events do not change that ownership.
func (s *journalStore) implementationBlocker(ctx context.Context, store *mekugiReplayStore, workspace, thread string, run journalRun) error {
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || !run.admitsRoot(*j) {
			return errors.New("implementation journal authority is unavailable")
		}
		if blocker := j.continuationBlocker(); blocker != nil {
			return fmt.Errorf("implementation journal blocked at %s: %s", blocker.Path, blocker.Reason)
		}
		return errJournalUnchanged
	})
}

func (run journalRun) admitsRoot(j threadJournal) bool {
	return j.IdentityKnown && !j.IdentityConflicted && j.Parent == "" && j.Author == "/root" &&
		j.Orchestration != nil && *j.Orchestration == run
}

func (run journalRun) validateCoordinator(read func(string, string) (threadJournal, bool, error)) error {
	main, exists, err := read(run.Workspace, run.Main)
	if !exists || err != nil || !run.admitsRoot(main) {
		return errors.New("orchestration coordinator journal authority is unavailable")
	}
	return nil
}

// Membership is independent of presentation, selection, and retention. Callers
// hold the journal/replay locks; Snapshot reads an atomic manifest without a run lock.
func (run journalRun) members(read func(string, string) (threadJournal, bool, error)) ([]journalRunMember, error) {
	if err := run.validateCoordinator(read); err != nil {
		return nil, err
	}
	batches, err := (&orchestrate.Store{Directory: run.Directory}).Snapshot(run.Workspace, run.Main)
	if err != nil {
		return nil, err
	}
	var members []journalRunMember
	for _, batch := range batches {
		if batch.Launch == nil || batch.Launch.ThreadID == "" {
			continue
		}
		child, exists, err := read(batch.Cwd, batch.Launch.ThreadID)
		if !exists || !run.admitsRoot(child) {
			err = errors.New("orchestration child journal authority is unavailable")
		}
		members = append(members, journalRunMember{batch: batch, journal: child, err: err})
	}
	return members, nil
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
	return scopeJournalBlocks(journals, failures, thread, blocks)
}

func scopeJournalBlocks(journals map[string]threadJournal, failures map[string]error, thread string, blocks []orchestrateEvent) []orchestrateEvent {
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

// Reuse retained authored events to repair publication interrupted after the
// journal save. The wait owner deduplicates queued and already selected IDs.
func (s *journalStore) orchestratedBlocks(ctx context.Context, store *mekugiReplayStore, run journalRun) ([]orchestrateEvent, error) {
	var blocks []orchestrateEvent
	err := s.transaction(ctx, store, run.Workspace, run.Main, func(j *threadJournal, exists bool) error {
		if !exists || j.Orchestration == nil {
			return errJournalUnchanged
		}
		if !run.admitsRoot(*j) {
			return errors.New("orchestration coordinator journal authority is unavailable")
		}
		journals, failures, err := s.relatedJournals(store.scoped(ctx), run.Workspace, run.Main)
		if err != nil {
			return err
		}
		for _, thread := range slices.Sorted(maps.Keys(journals)) {
			var owned []orchestrateEvent
			for _, event := range journals[thread].Events {
				if event.BlockID != "" {
					owned = append(owned, orchestrateEvent{ID: event.BlockID, Thread: thread, Turn: event.BlockTurn, Kind: "blocked", Status: "blocked", Path: event.Path, Message: event.Fields.Reason})
				}
			}
			blocks = append(blocks, scopeJournalBlocks(journals, failures, thread, owned)...)
		}
		return errJournalUnchanged
	})
	return blocks, err
}

func (s *journalStore) bindRun(ctx context.Context, store *mekugiReplayStore, workspace, thread string, run journalRun) error {
	_, err := s.bindRunOwned(ctx, store, workspace, thread, run)
	return err
}

// The caller can release only a new, unused binding if admission is canceled.
func (s *journalStore) bindRunOwned(ctx context.Context, store *mekugiReplayStore, workspace, thread string, run journalRun) (bool, error) {
	created := false
	err := s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || !j.IdentityKnown || j.IdentityConflicted || j.Parent != "" || j.Author != "/root" {
			return errors.New("orchestration journal requires a proven independent root")
		}
		if j.Orchestration != nil {
			if *j.Orchestration != run {
				return errors.New("journal orchestration reference changed")
			}
		}
		owned := j.Orchestration == nil
		if store != nil && run.Main == thread && run.Workspace == workspace {
			// Keep the authority locator with durable identity, before publishing
			// the journal binding. Missing journal payloads must not erase the role.
			scope, _, err := store.readHandleScope(thread)
			if err != nil {
				return err
			}
			if scope.Namespace != thread || scope.Parent != "" {
				return errors.New("coordinator requires an independent handle scope")
			}
			if scope.Coordinator != nil && *scope.Coordinator != run {
				return errors.New("retained coordinator reference changed")
			}
			if scope.Coordinator != nil {
				owned = false // Retained authority survives a missing journal binding.
			}
			if scope.Coordinator == nil {
				scope.Coordinator = &run
				if err := store.writeHandleScope(scope); err != nil {
					return err
				}
			}
		}
		if j.Orchestration != nil {
			return errJournalUnchanged
		}
		j.Orchestration = &run
		created = owned
		return nil
	})
	return created && err == nil, err
}

func (s *journalStore) releaseUnusedRun(ctx context.Context, store *mekugiReplayStore, run journalRun) error {
	return s.transaction(ctx, store, run.Workspace, run.Main, func(j *threadJournal, exists bool) error {
		if !exists || j.Orchestration == nil || *j.Orchestration != run {
			return errJournalUnchanged
		}
		batches, err := (&orchestrate.Store{Directory: run.Directory}).Snapshot(run.Workspace, run.Main)
		if err != nil {
			return err
		}
		if len(batches) != 0 {
			return errJournalUnchanged
		}
		if store != nil {
			scope, _, err := store.readHandleScope(run.Main)
			if err != nil {
				return err
			}
			if scope.Coordinator != nil && *scope.Coordinator == run {
				scope.Coordinator = nil
				if err := store.writeHandleScope(scope); err != nil {
					return err
				}
			}
		}
		j.Orchestration = nil
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
	members, err := run.members(func(workspace, thread string) (threadJournal, bool, error) {
		view := snapshot[workspace]
		journal, exists := view.journals[thread]
		return journal, exists, view.failures[thread]
	})
	if err != nil {
		return local, failures, err
	}
	member := root.Thread == run.Main && root.Workspace == run.Workspace
	for _, child := range members {
		if run.admitsRoot(child.journal) && child.journal.Thread == root.Thread && child.journal.Workspace == root.Workspace {
			member = true
		}
	}
	if !member {
		return local, failures, errors.New("journal root is not a confirmed member of its orchestration run")
	}

	mainJournals, mainErrors := snapshot[run.Workspace].journals, snapshot[run.Workspace].failures
	joined, errorsByThread := maps.Clone(mainJournals), maps.Clone(mainErrors)
	for _, member := range members {
		b, child := member.batch, member.journal
		if !run.admitsRoot(child) {
			continue // Unresolved members cannot prove joined journal ancestry.
		}
		journals, recordErrors := snapshot[b.Cwd].journals, snapshot[b.Cwd].failures
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

// Request membership uses the shared root authority without reading sibling journals.
// The reader runs under the journal and replay locks.
func journalRunMembership(workspace, caller string, read func(string, string) (threadJournal, bool, error)) (threadJournal, error) {
	seen := make(map[string]bool)
	valid := true
	var root threadJournal
	for thread := caller; ; {
		j, exists, err := read(workspace, thread)
		if err != nil {
			return threadJournal{}, err
		}
		if !exists || seen[thread] {
			return threadJournal{}, nil
		}
		valid = valid && j.IdentityKnown && !j.IdentityConflicted
		if j.Orchestration != nil && (!valid || j.Parent != "") {
			return threadJournal{}, errors.New("conflicted orchestration identity")
		}
		seen[thread] = true
		if j.Parent == "" {
			root = j
			break
		}
		thread = j.Parent
	}
	run := root.Orchestration
	if run == nil {
		return root, nil
	}
	batches, err := (&orchestrate.Store{Directory: run.Directory}).Snapshot(run.Workspace, run.Main)
	if err != nil {
		return threadJournal{}, err
	}
	member := root.Thread == run.Main && root.Workspace == run.Workspace
	for _, b := range batches {
		if b.Launch != nil && b.Launch.ThreadID == root.Thread && b.Cwd == root.Workspace {
			member = true
		}
	}
	if !member || !run.admitsRoot(root) {
		return threadJournal{}, errors.New("journal root is not a confirmed member of its orchestration run")
	}
	if err := run.validateCoordinator(read); err != nil {
		return threadJournal{}, err
	}
	return root, nil
}

// Resolve the requesting thread's retained cwd, not the Git-root metadata shape.
// Fresh descendants may use only their authenticated parent's retained scope.
func (s *journalStore) orchestrationRequestRoot(ctx context.Context, store *mekugiReplayStore, workspace, thread, parent string) (threadJournal, error) {
	store = store.scoped(ctx)
	release, err := s.lockState(ctx)
	if err != nil {
		return threadJournal{}, err
	}
	defer release()
	var root threadJournal
	resolve := func() error {
		read := func(workspace, thread string) (threadJournal, bool, error) {
			if store != nil {
				return readThreadJournal(store, workspace, thread)
			}
			j, exists := s.memory[journalKey(workspace, thread)]
			return j, exists, nil
		}
		// Ordinary selected roots keep their existing policy without a run lookup.
		if workspace != "" {
			j, exists, err := read(workspace, thread)
			if err != nil {
				return err
			}
			if exists && j.Parent == "" && j.Orchestration == nil {
				return nil
			}
		}
		// Discover scopes without applying compaction's identity gate. Ordinary
		// recovery may retain incomplete native ancestry; only a run-bearing
		// chain requires orchestration authorization here.
		scopes := []string{workspace}
		if store != nil {
			retained, err := store.orchestrationJournalScopes(thread)
			if err == nil && len(retained) == 0 && parent != "" {
				retained, err = store.orchestrationJournalScopes(parent)
			}
			if err != nil {
				return err
			}
			if len(retained) != 0 {
				scopes = retained
			}
		}
		for _, scope := range scopes {
			if scope == "" {
				continue
			}
			_, exists, err := read(scope, thread)
			if err != nil {
				return err
			}
			caller := thread
			if !exists && parent != "" {
				caller = parent
			}
			candidate, err := journalRunMembership(scope, caller, read)
			if err != nil {
				return err
			}
			if candidate.Orchestration == nil {
				continue
			}
			if len(scopes) != 1 {
				return errors.New("orchestration workspace is ambiguous")
			}
			if store != nil {
				// A proven run still requires the original unique retained scope,
				// including execution records outside its journal workspace.
				if _, err := store.compactionWorkspace(caller); err != nil && !errors.Is(err, errNoRetainedJournalWorkspace) {
					return err
				}
			}
			if workspace != "" {
				rel, err := filepath.Rel(workspace, scope)
				if err != nil || !filepath.IsLocal(rel) {
					return nil
				}
			}
			root = candidate
		}
		return nil
	}
	if store != nil {
		err = store.locked(ctx, resolve)
	} else {
		err = resolve()
	}
	return root, err
}

// Scope discovery is not membership. Only the requesting session's retained
// journals are candidates; journalRunMembership validates their run ancestry.
func (s *mekugiReplayStore) orchestrationJournalScopes(thread string) ([]string, error) {
	session, err := s.readRetainedSession(storageSessionName(thread))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var scopes []string
	for name := range session.Files {
		if !strings.HasPrefix(name, "journal-") {
			continue
		}
		j, _, err := readJournalRecord(filepath.Join(s.directory, name))
		if err != nil {
			return nil, err
		}
		if j.Thread == thread && filepath.IsAbs(j.Workspace) && filepath.Clean(j.Workspace) == j.Workspace {
			scopes = append(scopes, j.Workspace)
		}
	}
	return scopes, nil
}
