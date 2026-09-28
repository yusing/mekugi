package router

import (
	"cmp"
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type appServerWaitTarget struct {
	Thread string `json:"thread"`
	Name   string `json:"name"`
}

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
		directory, err := defaultMekugiReplayDirectory()
		if err != nil {
			return err
		}
		store, err = openMekugiReplayStoreContext(ctx, directory)
		if err != nil {
			return err
		}
	}
	ctx, release, err := store.beginSession(ctx, u.thread, "")
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
func (u *appServerUI) waitItem(item appServerItem, thread, turn, id string, started bool) appServerItem {
	if item.Type != "collabAgentToolCall" || item.Tool != "wait" || thread == "" || turn == "" || id == "" {
		return item
	}
	s := &u.session
	if s.waits == nil {
		s.waits = make(map[[3]string][]appServerWaitTarget)
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
			targets = append(targets, appServerWaitTarget{Thread: target, Name: cmp.Or(s.paths[target], target)})
		}
		if len(targets) == 0 && len(item.AgentsStates) == 0 {
			prefix := s.path(thread) + "/"
			for target, name := range s.paths {
				if agent := s.agent(name); agent != nil && agent.Responding && strings.HasPrefix(name, prefix) {
					targets = append(targets, appServerWaitTarget{Thread: target, Name: name})
				}
			}
			slices.SortFunc(targets, func(a, b appServerWaitTarget) int { return strings.Compare(a.Name, b.Name) })
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
	item.waitNames = make(map[string]string)
	for _, target := range targets {
		item.ReceiverThreadIDs = append(item.ReceiverThreadIDs, target.Thread)
		item.waitNames[target.Thread] = target.Name
	}
	for _, target := range receivers {
		if !slices.Contains(item.ReceiverThreadIDs, target) {
			item.ReceiverThreadIDs = append(item.ReceiverThreadIDs, target)
		}
	}
	for _, target := range item.ReceiverThreadIDs {
		if name := s.paths[target]; name != "" {
			item.waitNames[target] = name
		}
	}
	for target := range item.AgentsStates {
		if name := s.paths[target]; name != "" {
			item.waitNames[target] = name
		}
	}
	return item
}

func (s *appServerSession) waitTargetsPath(key [3]string) string {
	identity := s.cwd + "\x00" + strings.Join(key[:], "\x00")
	return filepath.Join(s.waitStore.directory, fmt.Sprintf("wait-%x.json", sha256.Sum256([]byte(identity))))
}

func (s *appServerSession) readWaitTargets(key [3]string) (targets []appServerWaitTarget, found bool, err error) {
	err = s.waitStore.locked(s.waitContext, func() error {
		path := s.waitTargetsPath(key)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("wait targets are not a regular file")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxNativeActivityMessageBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxNativeActivityMessageBytes {
			return errors.New("wait targets exceed size limit")
		}
		if err := json.Unmarshal(data, &targets, json.RejectUnknownMembers(true)); err != nil {
			return err
		}
		if err := s.waitStore.retainFiles(filepath.Base(path)); err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return targets, found, nil
}

func (s *appServerSession) writeWaitTargets(key [3]string, targets []appServerWaitTarget) error {
	if s.waitStore == nil {
		return nil
	}
	data, err := json.Marshal(&targets)
	if err != nil {
		return err
	}
	if len(data) > maxNativeActivityMessageBytes {
		return errors.New("wait targets exceed size limit")
	}
	return s.waitStore.locked(s.waitContext, func() error {
		return s.waitStore.writeManagedFile(filepath.Base(s.waitTargetsPath(key)), "wait-pending-", data)
	})
}
