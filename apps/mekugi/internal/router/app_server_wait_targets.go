package router

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/activity/waittargets"
)

func (u *appServerUI) openWaitStore(store *mekugiReplayStore) error {
	if u.waitRelease != nil {
		u.waitRelease()
		u.waitRelease = nil
	}
	ctx := u.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		directory, err := ReplayDirectory()
		if err != nil {
			return err
		}
		store, err = openReplayStore(ctx, u.history, nil, directory)
		if err != nil {
			return err
		}
	}
	ctx, release, err := store.beginSession(ctx, u.Thread, "")
	if err != nil {
		return err
	}
	u.session.waitStore, u.session.waitContext = store.scoped(ctx), ctx
	u.waitRelease = release
	return nil
}

// Mailbox waits omit receivers. Snapshot the running descendants at start,
// never the roster at completion or replay. These records are presentation
// evidence only: they cannot resume work or change the host's tool arguments.
func (u *appServerUI) waitItem(item binding.ThreadItem, thread, turn, id string, started bool) (binding.ThreadItem, map[string]string) {
	if item.Type != "collabAgentToolCall" || item.Tool != "wait" || thread == "" || turn == "" || id == "" {
		return item, nil
	}
	s := &u.session
	if s.waits == nil {
		s.waits = make(map[[3]string][]waittargets.Target)
	}
	key := [3]string{thread, turn, id}
	targets, known := s.waits[key]
	if !known && s.waitStore != nil {
		var err error
		targets, known, err = s.readWaitTargets(key)
		if err != nil {
			u.setNotice("Wait targets could not be restored: "+err.Error(), true)
		}
	}
	if !known && started {
		receivers := slices.Clone(item.ReceiverThreadIDs)
		var extra []string
		for target := range item.AgentsStates {
			if !slices.Contains(receivers, target) {
				extra = append(extra, target)
			}
		}
		slices.Sort(extra)
		for _, target := range append(receivers, extra...) {
			targets = append(targets, waittargets.Target{Thread: target, Name: cmp.Or(s.paths[target], target)})
		}
		if len(targets) == 0 && len(item.AgentsStates) == 0 {
			prefix := s.path(thread) + "/"
			for target, name := range s.paths {
				if agent := s.agent(name); agent != nil && agent.Responding && strings.HasPrefix(name, prefix) {
					targets = append(targets, waittargets.Target{Thread: target, Name: name})
				}
			}
			slices.SortFunc(targets, func(a, b waittargets.Target) int { return strings.Compare(a.Name, b.Name) })
		}
		if err := s.writeWaitTargets(key, targets); err != nil {
			u.setNotice("Wait targets could not be retained: "+err.Error(), true)
		}
	}
	if started || item.Status == "inProgress" {
		s.waits[key] = targets
	}
	receivers := item.ReceiverThreadIDs
	item.ReceiverThreadIDs = nil
	names := make(map[string]string)
	for _, target := range targets {
		item.ReceiverThreadIDs = append(item.ReceiverThreadIDs, target.Thread)
		names[target.Thread] = target.Name
	}
	for _, target := range receivers {
		if !slices.Contains(item.ReceiverThreadIDs, target) {
			item.ReceiverThreadIDs = append(item.ReceiverThreadIDs, target)
		}
	}
	for _, target := range item.ReceiverThreadIDs {
		if name := s.paths[target]; name != "" {
			names[target] = name
		}
	}
	for target := range item.AgentsStates {
		if name := s.paths[target]; name != "" {
			names[target] = name
		}
	}
	return item, names
}

func (s *appServerSession) waitTargetsPath(key [3]string) string {
	return filepath.Join(s.waitStore.directory, waittargets.Name(s.cwd, key[0], key[1], key[2]))
}

func (s *appServerSession) readWaitTargets(key [3]string) (targets []waittargets.Target, found bool, err error) {
	err = s.waitStore.locked(s.waitContext, func(store *mekugiReplayStore) error {
		path := s.waitTargetsPath(key)
		var err error
		targets, found, err = waittargets.Read(path)
		if err != nil || !found {
			return err
		}
		if err := store.retainFiles(filepath.Base(path)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return targets, found, nil
}

func (s *appServerSession) writeWaitTargets(key [3]string, targets []waittargets.Target) error {
	if s.waitStore == nil {
		return nil
	}
	data, err := waittargets.Encode(targets)
	if err != nil {
		return err
	}
	return s.waitStore.locked(s.waitContext, func(store *mekugiReplayStore) error {
		return store.writeManagedFile(filepath.Base(s.waitTargetsPath(key)), "wait-pending-", data)
	})
}
