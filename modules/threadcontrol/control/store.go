package control

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
	"sync"
	"sync/atomic"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Service borrows the selected history owner. Its indices contain admitted trees,
// not host processes, request correlation, or another retention store.
type Service struct {
	mu    sync.RWMutex
	trees map[string]*treeOwner
}

type treeOwner struct {
	session  history.Session
	mutation sync.Mutex
	state    atomic.Pointer[Tree]
}

const checkpointVersion = 4

func New() *Service {
	return &Service{trees: make(map[string]*treeOwner)}
}

// AdmitNamespace publishes authorized shared history before a new child can
// execute or expose evidence. It borrows the root's selected publication owner.
func (s *Service) AdmitNamespace(ctx context.Context, thread string) error {
	owner, err := s.owner(thread)
	if err != nil {
		return err
	}
	member := owner.state.Load().Members[thread]
	if member.Parent == "" {
		return errors.New("namespace admission requires a child")
	}
	return owner.session.Store.Transaction(ctx, owner.session.Writes, func(tx *history.Transaction) error {
		return tx.InitializeScope(history.Scope{Thread: thread, Namespace: owner.session.Namespace, Parent: member.Parent}, nil)
	})
}

func RecordKinds() []recordkind.Spec {
	return []recordkind.Spec{
		{ID: "threadcontrol", Match: func(name string) bool { return strings.HasPrefix(name, "threadcontrol-tree-") }, MaxRecordBytes: history.MaxRecordBytes, Dependencies: func(record recordkind.Record) ([]string, error) {
			var tree Tree
			if err := json.Unmarshal(record.Data, &tree); err != nil {
				return nil, err
			}
			var files []string
			for _, effect := range tree.Effects {
				if effect.Child != nil && effect.Child.Import != "" {
					files = append(files, effect.Child.Import)
				}
			}
			return files, nil
		}},
		{ID: "threadcontrol-import", Match: func(name string) bool { return strings.HasPrefix(name, "threadcontrol-import-") }, MaxRecordBytes: history.MaxRecordBytes},
	}
}

func recordName(root string) string {
	return fmt.Sprintf("threadcontrol-tree-%x", sha256.Sum256([]byte(root)))
}

// AdmitRoot borrows an already selected session. A fork's distinct root cannot
// acquire another root's control authority merely by inheriting its history.
func (s *Service) AdmitRoot(ctx context.Context, session history.Session, root *Member) error {
	if root == nil || session.Store == nil || session.Thread == "" || root.Thread != session.Thread || root.Parent != "" || root.Task != "/root" || root.Session == "" {
		return errors.New("invalid admitted thread-control root")
	}
	return s.admitRoot(ctx, session, root)
}

// Restore loads only the selected root's checkpoint. A fresh thread acquires
// settings and execution authority when its first actual request is admitted.
func (s *Service) Restore(ctx context.Context, session history.Session) error {
	if session.Store == nil || session.Thread == "" {
		return errors.New("recovery requires a selected history session")
	}
	if member, ok := s.Member(session.Thread); ok {
		if member.Parent != "" || member.AudienceAdmitted {
			return nil
		}
		return s.update(ctx, session.Thread, func(tree *Tree, _ *Member) error { return tree.selectRoot(session.Thread) })
	}
	return s.admitRoot(ctx, session, nil)
}

func (s *Service) admitRoot(ctx context.Context, session history.Session, root *Member) error {
	root, err := detach(root)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if _, ok := s.trees[session.Thread]; ok {
		s.mu.Unlock()
		return errors.New("thread-control root is already admitted")
	}
	owner := &treeOwner{session: session}
	s.trees[session.Thread] = owner
	s.mu.Unlock()
	defer func() {
		if owner.state.Load() == nil {
			s.mu.Lock()
			delete(s.trees, session.Thread)
			s.mu.Unlock()
		}
	}()
	state := Tree{Version: checkpointVersion, Root: session.Thread, Members: map[string]*Member{session.Thread: root}}
	data, err := history.ReadRecord(filepath.Join(session.Store.Directory(), recordName(session.Namespace)))
	if err == nil {
		if state, err = decodeTree(data, session.Namespace); err != nil {
			return err
		}
		pause(&state, "")
		if err := state.selectRoot(session.Thread); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if root == nil {
		return nil
	}
	if err = owner.publish(ctx, &state); err != nil {
		return err
	}
	return nil
}

func decodeTree(data []byte, root string) (Tree, error) {
	var state Tree
	if err := json.Unmarshal(data, &state); err != nil {
		return Tree{}, fmt.Errorf("decode thread-control checkpoint: %w", err)
	}
	if state.Version != checkpointVersion || state.Members[root] == nil || state.Members[root].Parent != "" || state.Members[root].Task != "/root" || state.Members[state.Root] == nil || state.Members[state.Root].Parent != "" || state.Members[state.Root].Task != "/root" {
		return Tree{}, errors.New("thread-control checkpoint identity mismatch")
	}
	for id, member := range state.Members {
		if member == nil || id != member.Thread || member.Session == "" {
			return Tree{}, errors.New("invalid retained member identity")
		}
	}
	return state, nil
}

// ReadFamily returns detached metadata from the selected retained namespace.
// It neither resumes coordination nor opens a writable history session.
func ReadFamily(store *history.Store, thread string) ([]Participant, int, error) {
	if store == nil || thread == "" {
		return nil, 0, errors.New("retained family requires a history store and thread")
	}
	root, err := store.NamespaceForThread(thread)
	if err != nil {
		return nil, 0, err
	}
	data, err := history.ReadRecord(filepath.Join(store.Directory(), recordName(root)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	state, err := decodeTree(data, root)
	if err != nil {
		return nil, len(data), err
	}
	selected := state.Members[thread]
	if selected == nil || !selected.AudienceAdmitted && (selected.Parent != "" || selected.Task != "/root") {
		return nil, len(data), errors.New("selected thread is not an admitted retained family member")
	}
	root = state.Root
	if selected.Parent == "" {
		root = selected.Thread
	}
	var members []Participant
	for _, member := range state.Members {
		if member.Thread == root || member.AudienceAdmitted && member.Parent != "" {
			participant := member.participant()
			if participant.Parent == state.Root {
				participant.Parent = root
			}
			members = append(members, participant)
		}
	}
	slices.SortFunc(members, func(a, b Participant) int { return strings.Compare(a.Task, b.Task) })
	return members, len(data), nil
}

func (o *treeOwner) publish(ctx context.Context, state *Tree) error {
	data, err := json.Marshal(state, json.Deterministic(true))
	if err != nil {
		return err
	}
	if len(data) > history.MaxRecordBytes {
		return errors.New("thread-control checkpoint exceeds retained record limit")
	}
	name := recordName(o.session.Namespace)
	err = o.session.Store.Transaction(ctx, o.session.Writes, func(tx *history.Transaction) error {
		if err := tx.Admit(o.session.Limits, name, int64(len(data))); err != nil {
			return err
		}
		if err := tx.Write(name, "threadcontrol-pending-", data); err != nil {
			return err
		}
		return tx.Retain(o.session.Thread, o.session.Limits, name)
	})
	if err == nil {
		o.state.Store(state)
	}
	return err
}

var ErrNotAdmitted = errors.New("caller is not admitted to this invocation")

func (s *Service) owner(thread string) (*treeOwner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, owner := range s.trees {
		if state := owner.state.Load(); state != nil {
			if _, ok := state.Members[thread]; ok {
				return owner, nil
			}
		}
	}
	return nil, ErrNotAdmitted
}

// update replaces only touched members; publication freezes new caller payloads.
func (s *Service) update(ctx context.Context, thread string, mutate func(*Tree, *Member) error) error {
	owner, err := s.owner(thread)
	if err != nil {
		return err
	}
	owner.mutation.Lock()
	defer owner.mutation.Unlock()
	next := *owner.state.Load()
	next.Members, next.Effects = maps.Clone(next.Members), slices.Clone(next.Effects)
	for i := range next.Effects {
		if child := next.Effects[i].Child; child != nil {
			next.Effects[i].Child = new(*child)
		}
	}
	if err := mutate(&next, next.edit(thread)); err != nil {
		if err == unchanged {
			return nil
		}
		return err
	}
	return owner.publish(ctx, &next)
}

var unchanged = errors.New("unchanged coordination state")

func admitted(tree *Tree, caller Identity) (*Member, error) {
	member, ok := tree.Members[caller.Thread]
	if !ok || caller.Session == "" || caller.Session != member.Session || caller.Turn == "" || caller.Turn != member.Turn || !member.HostRunning {
		return nil, errors.New("missing, stale or foreign thread-control caller")
	}
	return member, nil
}

// Snapshot returns detached display/read values without waiting for slow storage.
func (s *Service) Snapshot(thread string) (Tree, error) {
	owner, err := s.owner(thread)
	if err != nil {
		return Tree{}, err
	}
	return detach(*owner.state.Load())
}

// WaitForTurn borrows the admitted publication and detaches only the selected
// wait, avoiding full-tree copies on provider continuations.
func (s *Service) WaitForTurn(thread, turn string) (*Wait, error) {
	member, ok := s.member(thread)
	if !ok {
		return nil, ErrNotAdmitted
	}
	if !member.HostRunning || member.Turn != turn || member.Wait == nil || member.Wait.Turn != turn || member.Wait.Generation != member.Generation {
		return nil, nil
	}
	return detach(member.Wait)
}

func detach[T any](value T) (result T, err error) {
	data, err := json.Marshal(&value)
	if err == nil {
		err = json.Unmarshal(data, &result)
	}
	return result, err
}

// Member is a detached presentation/lifecycle read. It grants no tool authority.
func (s *Service) Member(thread string) (*Member, bool) {
	member, ok := s.member(thread)
	if !ok {
		return nil, false
	}
	member, err := detach(member)
	return member, err == nil
}

// ObserveAudience publishes canonical ancestry while excluding root replacement.
// The callback must only update the audience, never call back into coordination.
// Confirmed children can enter their audience before admission is acknowledged.
func (s *Service) ObserveAudience(thread string, observe func(string, string, string, bool) bool) bool {
	owner, err := s.owner(thread)
	if err != nil {
		return false
	}
	owner.mutation.Lock()
	defer owner.mutation.Unlock()
	tree := owner.state.Load()
	member := tree.Members[thread]
	if member.Parent == "" && tree.Root != thread {
		return false
	}
	return observe(member.Thread, member.Parent, member.Task, member.Parent != "")
}

// ContextPreloads borrows admitted ancestry without copying the logical tree or
// its growing effect history. Consumers match the retained assignment item before
// acquiring anything, including when a full-history child inherits that item.
func (s *Service) ContextPreloads(thread, session string) ([]ContextPreload, error) {
	owner, err := s.owner(thread)
	if errors.Is(err, ErrNotAdmitted) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tree := owner.state.Load()
	member := tree.Members[thread]
	if !member.AudienceAdmitted || member.Session != session {
		return nil, errors.New("context preload requires admitted runtime identity")
	}
	var selections []ContextPreload
	for member != nil {
		if member.Preload.Selection != "" {
			selections = append(selections, member.Preload)
		}
		member = tree.Members[member.Parent]
	}
	return selections, nil
}

// Descendants reads admitted logical members, including cold paused children.
func (s *Service) Descendants(thread string) []Participant {
	owner, err := s.owner(thread)
	if err != nil {
		return nil
	}
	tree := owner.state.Load()
	prefix := tree.Members[thread].Task + "/"
	var result []Participant
	for _, member := range tree.Members {
		if member.AudienceAdmitted && strings.HasPrefix(member.Task, prefix) {
			result = append(result, member.participant())
		}
	}
	slices.SortFunc(result, func(a, b Participant) int { return strings.Compare(a.Task, b.Task) })
	return result
}

// Family publishes only admitted navigation metadata, independently of runtime
// lifetime ancestry. Replacing a runtime's thread does not reparent its peers.
func (s *Service) Family(thread string) (string, []Participant) {
	owner, err := s.owner(thread)
	if err != nil {
		return "", nil
	}
	tree := owner.state.Load()
	members := make([]Participant, 0, len(tree.Members))
	for _, member := range tree.Members {
		if member.AudienceAdmitted {
			members = append(members, member.participant())
		}
	}
	slices.SortFunc(members, func(a, b Participant) int { return strings.Compare(a.Task, b.Task) })
	return tree.Root, members
}

func (m *Member) participant() Participant {
	return Participant{
		Thread: m.Thread, Parent: m.Parent, Task: m.Task, Role: m.Settings.Role, Status: m.Status,
		NonAttachable: m.Settings.NonAttachable,
	}
}

// member borrows only the current immutable publication, never the storage lock.
func (s *Service) member(thread string) (*Member, bool) {
	owner, err := s.owner(thread)
	if err != nil {
		return nil, false
	}
	return owner.state.Load().Members[thread], true
}

func (s *Service) Due(now time.Time) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var threads []string
	for _, owner := range s.trees {
		if state := owner.state.Load(); state != nil {
			for thread, member := range state.Members {
				if wait := member.Wait; wait != nil && member.Status == Waiting && !member.HostRunning && wait.State == "registered" && wait.Deadline != nil && !now.Before(*wait.Deadline) {
					threads = append(threads, thread)
				}
			}
		}
	}
	return threads
}
