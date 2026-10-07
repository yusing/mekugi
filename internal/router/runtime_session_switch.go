package router

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

type runtimeSessionCompanion struct {
	target       ObservationBinding
	registry     *toolRegistry
	presentation CompanionPresentation
	token        string
}

func (p *runtimeSessionCompanion) close() error {
	return errors.Join(p.registry.Close(), os.RemoveAll(p.presentation.Plugin))
}

// The native bridge calls this only after it closes and drains the old query.
// Resume restores the selected namespace; clear selects a new empty namespace.
// Neither inherits authority or evidence from the departing session.
func (s *ObservationService) switchSession(ctx context.Context, source, target ObservationBinding, check bool) error {
	o := s.owner
	o.mu.Lock()
	if source.Runtime != o.runtime || source.Workspace != o.workspace || source.Session != o.session || source.Agent != "" || source.Branch != "" ||
		target.Runtime != o.runtime || !filepath.IsAbs(target.Workspace) || target.Session == "" || len(target.Session) > 1024 || target.Session == source.Session || target.Agent != "" || target.Branch != "" {
		o.mu.Unlock()
		return errors.New("session transition requires the current root and a distinct native session with an absolute workspace")
	}
	if o.pendingCount.Load() != 0 {
		o.mu.Unlock()
		return errors.New("session transition waits for native observations to settle")
	}
	workspace, err := filepath.EvalSymlinks(target.Workspace)
	if err != nil || workspace != target.Workspace {
		o.mu.Unlock()
		return errors.Join(err, errors.New("native session workspace must be a resolved directory"))
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		o.mu.Unlock()
		return errors.Join(err, errors.New("native session workspace is not a directory"))
	}
	lease := flock.New(filepath.Join(o.store.directory, observationThread(target)+".observation.lock"), flock.SetPermissions(0600))
	available, err := lease.TryLock()
	if err != nil || !available {
		o.mu.Unlock()
		return errors.Join(err, errors.New("native session already has an observation owner"))
	}
	_ = lease.Unlock()
	if s.stagedCompanion != nil && s.stagedCompanion.target != target {
		if err := s.stagedCompanion.close(); err != nil {
			o.mu.Unlock()
			return err
		}
		s.stagedCompanion = nil
	}
	if target.Workspace != o.workspace && s.registry != nil && s.stagedCompanion == nil {
		token := rand.Text()
		presentation, registry, err := s.prepareCompanion(ctx, target.Workspace, token, filepath.Join(s.directory, "plugin-"+rand.Text()))
		if err != nil {
			o.mu.Unlock()
			return err
		}
		s.stagedCompanion = &runtimeSessionCompanion{target: target, registry: registry, presentation: presentation, token: token}
	}
	if check {
		o.mu.Unlock()
		return nil
	}
	o.retireBindingsLocked(target.Session)
	o.workspace = target.Workspace
	if s.journal != nil {
		s.journal.detachSession()
	}
	err = o.bindLocked(ctx, target)
	var old *runtimeSessionCompanion
	if err == nil && s.stagedCompanion != nil {
		old = &runtimeSessionCompanion{registry: s.registry, presentation: CompanionPresentation{Plugin: s.plugin}}
		s.registry, s.plugin, s.frontendToken = s.stagedCompanion.registry, s.stagedCompanion.presentation.Plugin, s.stagedCompanion.token
		s.stagedCompanion = nil
	}
	o.mu.Unlock()
	if err != nil {
		return err
	}
	if old != nil {
		defer old.close()
	}
	if s.journal != nil {
		return s.journal.bind(ctx, target)
	}
	return nil
}

// Release only the drained session's leases, not launch-wide snapshots or the
// companion service. This permits A → B → A without borrowing a live owner.
func (o *nativeObservationOwner) retireBindingsLocked(session string) {
	for _, release := range o.release {
		release()
	}
	o.release = nil
	o.session = session
	clear(o.bindings)
	clear(o.tasks)
	clear(o.taskEvents)
	clear(o.live)
	clear(o.pending)
	o.windows = &execWindowRegistry{}
	o.broker.resync(liveDiffScope{Workspaces: map[string]map[string]bool{}})
}
