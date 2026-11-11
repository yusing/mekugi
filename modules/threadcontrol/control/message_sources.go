package control

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// CompletionSelection discovers only captured-change references. The caller
// supplies completion authority; ordinary message text never grants it.
func CompletionSelection(message string) string {
	var sources []string
	seen := make(map[string]bool)
	for ref := range attachments.References(message) {
		if strings.HasPrefix(ref, "mchanges:") && !seen[ref] {
			sources = append(sources, ref)
			seen[ref] = true
		}
	}
	if len(sources) == 0 {
		return ""
	}
	data, _ := json.Marshal(struct {
		Sources []string `json:"sources"`
	}{sources})
	return string(data)
}

type messageSnapshot struct {
	Session, Thread, Turn, Call, Route, Original, Captured string
}

// FreezeMessage retains source bytes separately from the growing logical tree.
// Repeated authenticated operations reuse their exact snapshot, including failures.
// Acquisition never holds the tree mutation lock or a history transaction.
func (s *Service) FreezeMessage(ctx context.Context, caller Identity, route, message string, acquire func() (string, error)) (string, error) {
	if _, err := s.Caller(caller); err != nil {
		return "", err
	}
	return s.freezeMessage(ctx, caller.Thread, caller, route, message, acquire)
}

// FreezeAssignmentSources borrows the already admitted spawn's authority, so
// recovery does not require its original parent turn to remain running.
func (s *Service) FreezeAssignmentSources(ctx context.Context, effect Effect, acquire func() (string, error)) (string, error) {
	current, err := s.Effect(effect.Caller, effect.ID)
	if err != nil {
		return "", err
	}
	if current == nil || current.Kind != "create" || current.Phase != "assignment_input" || current.State != "dispatching" || current.Target != effect.Target || current.Message != effect.Message {
		return "", errors.New("source capture requires a claimed assignment")
	}
	member, ok := s.Member(effect.Caller)
	if !ok {
		return "", ErrNotAdmitted
	}
	caller := Identity{Thread: effect.Caller, Session: member.Session, Turn: effect.CallerTurn, Call: effect.ID}
	return s.freezeMessage(ctx, effect.Caller, caller, "spawn:"+effect.Target, effect.Message, acquire)
}

// FreezePeerMessage captures into the recipient's existing retained namespace.
// The active-session peer resolver supplies the authenticated foreign origin.
func (s *Service) FreezePeerMessage(ctx context.Context, source Origin, target, call, message string, acquire func() (string, error)) (string, error) {
	receiver, err := s.RemoteTarget(target)
	if err != nil {
		return "", err
	}
	if source.Session == receiver.Session || source.Thread == "" {
		return "", errors.New("foreign snapshot requires an authenticated foreign origin")
	}
	caller := Identity{Thread: source.Thread, Session: source.Session, Turn: source.Turn, Call: call}
	return s.freezeMessage(ctx, target, caller, "peer:"+target, message, acquire)
}

func (s *Service) freezeMessage(ctx context.Context, thread string, caller Identity, route, message string, acquire func() (string, error)) (string, error) {
	if caller.Call == "" {
		return "", errors.New("message snapshot requires an operation identity")
	}
	owner, err := s.owner(thread)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("threadcontrol-message-%x", sha256.Sum256([]byte(caller.Thread+"\x00"+caller.Call)))
	for {
		ready := make(chan struct{})
		prior, loaded := owner.messageReads.LoadOrStore(name, ready)
		if !loaded {
			defer func() {
				owner.messageReads.Delete(name)
				close(ready)
			}()
			break
		}
		select {
		case <-prior.(chan struct{}):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	wanted := messageSnapshot{Session: caller.Session, Thread: caller.Thread, Turn: caller.Turn, Call: caller.Call, Route: route, Original: message}
	path := filepath.Join(owner.session.Store.Directory(), name)
	data, err := history.ReadRecord(path)
	if err == nil {
		var saved messageSnapshot
		if err := json.Unmarshal(data, &saved); err != nil {
			return "", err
		}
		saved.Captured = ""
		if saved != wanted {
			return "", errors.New("conflicting repeated message snapshot")
		}
		return name, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	wanted.Captured, err = acquire()
	if err != nil {
		return "", err
	}
	data, err = json.Marshal(wanted)
	if err != nil {
		return "", err
	}
	err = owner.session.Store.Transaction(ctx, owner.session.Writes, func(tx *history.Transaction) error {
		if err := tx.Admit(owner.session.Limits, name, int64(len(data))); err != nil {
			return err
		}
		if err := tx.Write(name, "threadcontrol-message-pending-", data); err != nil {
			return err
		}
		return tx.Retain(thread, owner.session.Limits, name)
	})
	return name, err
}

// MessageText materializes a previously frozen message only at delivery or
// confirmation, keeping attached bodies out of the copied coordination tree.
func (s *Service) MessageText(thread, reference, original string) (string, error) {
	if filepath.Base(reference) != reference || !strings.HasPrefix(reference, "threadcontrol-message-") {
		return "", errors.New("invalid message snapshot reference")
	}
	owner, err := s.owner(thread)
	if err != nil {
		return "", err
	}
	data, err := history.ReadRecord(filepath.Join(owner.session.Store.Directory(), reference))
	if err != nil {
		return "", err
	}
	var saved messageSnapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return "", err
	}
	if saved.Original != original {
		return "", errors.New("message snapshot does not match its retained message")
	}
	return original + "\n\nReferenced sources captured when sent:\n" + saved.Captured, nil
}

func (s *Service) RetainActionSources(ctx context.Context, caller, id, sources string) error {
	return s.update(ctx, caller, func(tree *Tree, _ *Member) error {
		effect := tree.Effect(caller, id)
		if effect == nil || effect.Kind != "create" || effect.Phase != "assignment_input" || effect.State != "dispatching" {
			return errors.New("source attachment requires a claimed assignment")
		}
		if effect.Sources != "" {
			if effect.Sources != sources {
				return errors.New("conflicting assignment sources")
			}
			return unchanged
		}
		effect.Sources = sources
		return nil
	})
}
