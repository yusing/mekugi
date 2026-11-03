package control

import (
	"context"
	"errors"
	"fmt"
)

// Peer is borrowed from the active runtime registry after caller authentication.
// Foreign members never enter the caller's logical tree.
type Peer struct {
	Source  Origin
	Target  RemoteTarget
	Control *Service
	Runtime Runtime
	Done    <-chan struct{}
	Deliver func(context.Context, string) error
}

type PeerResolver interface {
	Peer(Identity, string) (Peer, error)
}

// Origin freezes server-established attribution, separately from message text.
type Origin struct {
	Session, Thread, Task, Turn string
	Generation                  uint64
	Status                      Status
}

func (o Origin) Sender() string {
	return fmt.Sprintf("%s (session %s, thread %s)", AgentName(o.Task), o.Session, o.Thread)
}

type RemoteTarget struct {
	Session, Thread, Task, Turn string
	Generation                  uint64
	Status                      Status
	HostRunning                 bool
	Unavailable                 bool
}

func (s *Service) Origin(caller Identity) (Origin, error) {
	owner, err := s.owner(caller.Thread)
	if err != nil {
		return Origin{}, err
	}
	tree := owner.state.Load()
	member, err := admitted(tree, caller)
	if err != nil {
		return Origin{}, err
	}
	return Origin{Session: tree.Root, Thread: member.Thread, Task: member.Task, Turn: member.Turn, Generation: member.Generation, Status: member.Status}, nil
}

func (s *Service) RemoteTarget(thread string) (RemoteTarget, error) {
	owner, err := s.owner(thread)
	if err != nil {
		return RemoteTarget{}, err
	}
	tree := owner.state.Load()
	m := tree.Members[thread]
	if !m.AudienceAdmitted || m.Parent == "" && tree.Root != thread {
		return RemoteTarget{}, errors.New("target has no active coordination identity")
	}
	return RemoteTarget{Session: tree.Root, Thread: m.Thread, Task: m.Task, Turn: m.Turn, Generation: m.Generation, Status: m.Status, HostRunning: m.HostRunning}, nil
}

// Changes closes after the next immutable publication. Read selected facts after
// acquiring it to avoid losing a publication between inspection and subscription.
func (s *Service) Changes(thread string) (<-chan struct{}, error) {
	owner, err := s.owner(thread)
	if err != nil {
		return nil, err
	}
	owner.mutation.Lock()
	defer owner.mutation.Unlock()
	return owner.changed, nil
}

// ReceiveMessage retains foreign input at the recipient's existing effect owner.
// Its local dispatch identity permits ordinary claims, reconciliation and consume.
func (s *Service) ReceiveMessage(ctx context.Context, source Origin, target, call, message string) (result Effect, err error) {
	err = s.update(ctx, target, func(tree *Tree, member *Member) error {
		if source.Session == tree.Root || source.Thread == "" || call == "" || !member.AudienceAdmitted {
			return errors.New("foreign message requires authenticated source and recipient")
		}
		id := source.Thread + ":" + call
		if old := tree.Effect(target, id); old != nil {
			original := source
			if old.Origin != nil {
				original.Status = old.Origin.Status
			}
			if old.Origin == nil || *old.Origin != original || old.Message != message || old.Kind != "message" {
				return errors.New("conflicting repeated foreign message")
			}
			result = *old
			return unchanged
		}
		result = Effect{ID: id, Caller: target, CallerTurn: source.Turn, CallerGeneration: source.Generation, Target: target, Kind: "message", Message: message, State: "pending", Generation: member.Generation, Origin: &source}
		tree.Effects = append(tree.Effects, result)
		return nil
	})
	return
}

func (effect Effect) source() string {
	if effect.Origin != nil {
		return effect.Origin.Thread
	}
	return effect.Caller
}

func (tree *Tree) waitMember(wait *Wait, id string) *Member {
	if member := tree.Members[id]; member != nil {
		return member
	}
	if remote, ok := wait.Foreign[id]; ok {
		return &Member{Thread: id, Session: remote.Session, Task: id, Turn: remote.Turn, Generation: remote.Generation, Status: remote.Status, HostRunning: remote.HostRunning}
	}
	return nil
}

// ForeignWait borrows only immutable selected status facts, never a foreign tree.
func (s *Service) ForeignWait(thread, id string) map[string]RemoteTarget {
	member, ok := s.member(thread)
	if !ok || member.Wait == nil || member.Wait.ID != id || member.Wait.State != "registered" {
		return nil
	}
	return member.Wait.Foreign
}

func (s *Service) RefreshForeignWait(ctx context.Context, thread, id string, state RemoteTarget) error {
	return s.update(ctx, thread, func(_ *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.ID != id || wait.State != "registered" {
			return unchanged
		}
		old, ok := wait.Foreign[state.Thread]
		if !ok || old == state {
			return unchanged
		}
		if old.Session != state.Session {
			return errors.New("foreign wait target identity changed")
		}
		wait.Foreign[state.Thread] = state
		return nil
	})
}

func (tree *Tree) stoppedEvent(wait *Wait, member *Member) *Event {
	event := &Event{ID: wait.ID + ":stopped:" + member.Thread, Recipient: wait.Caller, Source: member.Thread, Task: member.Task, Turn: member.Turn, Generation: member.Generation, Kind: string(member.Status), Status: member.Status}
	if remote, ok := wait.Foreign[member.Thread]; ok {
		event.Session = remote.Session
		if remote.Unavailable {
			event.Kind, event.Message = "unavailable", "Selected foreign session runtime ended"
		}
	}
	return event
}
