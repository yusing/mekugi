package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

func resolve(tree *Tree, caller *Member, target string) (*Member, error) {
	if target == "" {
		return nil, errors.New("empty task target")
	}
	if member := tree.Members[target]; member != nil {
		if member.Parent == "" && member.Thread != tree.Root {
			return nil, errors.New("target is a previous root branch")
		}
		return member, nil
	}
	candidate := TaskPath(target)
	if !strings.HasPrefix(candidate, "/") {
		candidate = caller.Task + "/" + candidate
	}
	for _, member := range tree.Members {
		if member.Task == candidate && (member.Parent != "" || member.Thread == tree.Root) {
			return member, nil
		}
	}
	var result *Member
	for _, member := range tree.Members {
		if !strings.HasPrefix(target, "/") && strings.HasSuffix(member.Task, "/"+target) {
			if result != nil {
				return nil, fmt.Errorf("ambiguous task %q", target)
			}
			result = member
		}
	}
	if result == nil {
		return nil, fmt.Errorf("unknown task %q", target)
	}
	return result, nil
}

func descendants(tree *Tree, caller *Member, targets []string, all bool) ([]string, error) {
	selected := []string{}
	if all {
		for id, member := range tree.Members {
			if strings.HasPrefix(member.Task, caller.Task+"/") {
				selected = append(selected, id)
			}
		}
		slices.Sort(selected)
	} else {
		for _, target := range targets {
			member, err := resolve(tree, caller, target)
			if err != nil {
				return nil, err
			}
			if !strings.HasPrefix(member.Task, caller.Task+"/") {
				return nil, fmt.Errorf("wait target %q is not an admitted descendant", target)
			}
			if !slices.Contains(selected, member.Thread) {
				selected = append(selected, member.Thread)
			}
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("wait selection is empty")
	}
	return selected, nil
}

func selectedEvent(tree *Tree, wait *Wait) *Effect {
	for i := range tree.Effects {
		effect := &tree.Effects[i]
		if effect.delivered() && !effect.Consumed && effect.Target == wait.Caller && effect.ClaimedBy == "" && slices.Contains(wait.Targets, effect.Caller) {
			return effect
		}
	}
	return nil
}

func (tree *Tree) waitResult(wait *Wait) *WaitResult {
	result := &WaitResult{ID: wait.ID, Standalone: wait.Standalone, All: wait.All, Mode: wait.Mode, Events: slices.Clone(wait.Terminals), State: wait.PublicState(), EffectiveMS: wait.EffectiveMS, Metrics: wait.Metrics}
	if wait.Standalone {
		result.Mode = ""
	}
	if compact := wait.Compaction; compact != nil {
		result.Compaction = &CompactionResult{State: compact.State, Error: compact.Error}
		if compact.State == "registered" {
			result.State = "scheduled"
		}
	}
	if wait.Terminal != nil {
		result.Event = new(*wait.Terminal)
	}
	for _, target := range wait.Targets {
		if member := tree.Members[target]; member != nil {
			result.Targets = append(result.Targets, member.Task)
		}
	}
	for _, effect := range tree.Effects {
		if effect.delivered() && effect.ClaimedBy == wait.ID && effect.Target == wait.Caller {
			if wait.Mode == WaitAll && effect.Kind == "final" {
				result.Events = append(result.Events, *tree.event(effect))
			} else {
				result.Event = tree.event(effect)
				break
			}
		}
	}
	return result
}

// Prefer ordered delivered events. A stopped target still settles a wait when
// its notification was already read, was addressed to its own parent, or cannot
// arrive because its runtime was paused. Pending direct delivery keeps ordering.
func (tree *Tree) selectWaitOutcome(wait *Wait) bool {
	if wait.Mode == WaitAll {
		return tree.selectAllWaitOutcomes(wait)
	}
	if event := selectedEvent(tree, wait); event != nil {
		event.ClaimedBy = wait.ID
		return true
	}
	if len(wait.Recovery) != 0 {
		for i := range tree.Effects {
			effect := &tree.Effects[i]
			generation, selected := wait.Recovery[effect.Caller]
			if selected && effect.Kind == "final" && effect.delivered() && generation == effect.CallerGeneration {
				// A terminal coordinator cannot relay a fresh nested result. Observe
				// the captured work's outcome without claiming its recipient's input.
				wait.Terminal = tree.event(*effect)
				wait.Terminal.Recipient = wait.Caller
				return true
			}
		}
		return false // Only captured active work can supply a terminal-outcome wake.
	}
	for _, id := range wait.Targets {
		member := tree.Members[id]
		if member == nil || member.HostRunning || member.Status != Completed && member.Status != Failed && member.Status != Interrupted && member.Status != Paused {
			continue
		}
		pending := false
		for _, effect := range tree.Effects {
			if effect.Kind == "final" && effect.Caller == id && effect.Target == wait.Caller && effect.CallerGeneration == member.Generation && (effect.State == "pending" || hostOwned(effect.State)) {
				pending = true
				break
			}
		}
		if pending {
			continue
		}
		wait.Terminal = &Event{ID: wait.ID + ":stopped:" + id, Recipient: wait.Caller, Source: id, Task: member.Task, Turn: member.Turn, Generation: member.Generation, Kind: string(member.Status), Status: member.Status}
		return true
	}
	return false
}

// Select atomically: incomplete all-waits leave notifications available for
// message wakes, cancellation and later waits. Scan retained effects once.
func (tree *Tree) selectAllWaitOutcomes(wait *Wait) bool {
	selected := make(map[string]bool, len(wait.Targets))
	for _, id := range wait.Targets {
		selected[id] = true
	}
	outcomes := make(map[string]*Effect, len(wait.Targets))
	pending := make(map[string]bool)
	for i := range tree.Effects {
		effect := &tree.Effects[i]
		if effect.Target != wait.Caller || !selected[effect.Caller] {
			continue
		}
		if effect.Kind == "message" && effect.delivered() && !effect.Consumed && effect.ClaimedBy == "" {
			effect.ClaimedBy = wait.ID
			return true
		}
		member := tree.Members[effect.Caller]
		if effect.Kind != "final" || member == nil || effect.CallerGeneration != member.Generation {
			continue
		}
		if effect.State == "pending" || hostOwned(effect.State) {
			pending[effect.Caller] = true
		}
		if effect.delivered() && !effect.Consumed && effect.ClaimedBy == "" {
			outcomes[effect.Caller] = effect
		}
	}
	for _, id := range wait.Targets {
		member := tree.Members[id]
		if member == nil || member.HostRunning || member.Status != Completed && member.Status != Failed && member.Status != Interrupted && member.Status != Paused || pending[id] {
			return false
		}
	}
	for _, id := range wait.Targets {
		if effect := outcomes[id]; effect != nil {
			effect.ClaimedBy = wait.ID
		} else {
			member := tree.Members[id]
			wait.Terminals = append(wait.Terminals, Event{ID: wait.ID + ":stopped:" + id, Recipient: wait.Caller, Source: id, Task: member.Task, Turn: member.Turn, Generation: member.Generation, Kind: string(member.Status), Status: member.Status})
		}
	}
	return true
}

func (s *Service) RegisterWait(ctx context.Context, caller Identity, targets []string, all bool, mode WaitMode, timeout *int64, now time.Time) (result WaitResult, err error) {
	return s.registerWait(ctx, caller, targets, all, mode, timeout, now, false, false)
}

// RegisterCompaction registers the checkpoint and optional wait in one transaction.
func (s *Service) RegisterCompaction(ctx context.Context, caller Identity, targets []string, all bool, mode WaitMode, timeout *int64, now time.Time, standalone bool) (WaitResult, error) {
	return s.registerWait(ctx, caller, targets, all, mode, timeout, now, true, standalone)
}

func (s *Service) registerWait(ctx context.Context, caller Identity, targets []string, all bool, mode WaitMode, timeout *int64, now time.Time, compact, standalone bool) (result WaitResult, err error) {
	if mode == "" {
		mode = WaitAny
	}
	if mode != WaitAny && mode != WaitAll {
		return result, errors.New("wait_mode must be any or all")
	}
	err = s.update(ctx, caller.Thread, func(tree *Tree, member *Member) error {
		if _, err := admitted(tree, caller); err != nil {
			return err
		}
		if caller.Call == "" || caller.Item == "" {
			return errors.New("wait requires enclosing host-call identity")
		}
		var selected []string
		if !standalone {
			var err error
			selected, err = descendants(tree, member, targets, all)
			if err != nil {
				return err
			}
		}
		if _, err := tree.transitionHost(member, hostEvent{kind: hostEventWaitRegistration, turn: caller.Turn, item: caller.Item, call: caller.Call, compact: compact, standalone: standalone}); err != nil {
			if err == unchanged {
				result = *tree.waitResult(member.Wait)
			}
			return err
		}
		wait := &Wait{ID: caller.Call, Caller: caller.Thread, Target: caller.Thread, Generation: member.Generation, Turn: caller.Turn, CallerTurn: caller.Turn, Kind: "wake", State: "registered", Item: caller.Item, Targets: selected, All: all, Mode: mode}
		wait.Standalone = standalone
		if compact {
			wait.Compaction = &Effect{ID: caller.Call + ":compact", Caller: caller.Thread, Target: caller.Thread, CallerTurn: caller.Turn, Generation: member.Generation, Kind: "compact", State: "registered"}
		}
		if timeout != nil {
			if *timeout > member.Limits.MaxWaitMS {
				return errors.New("wait timeout exceeds the captured host maximum")
			}
			effective := max(*timeout, member.Limits.MinWaitMS)
			if effective < 0 || effective > int64((1<<63-1)/int64(time.Millisecond)) {
				return errors.New("invalid captured host wait timeout bounds")
			}
			wait.RequestedMS, wait.EffectiveMS, wait.Deadline = new(*timeout), new(effective), new(now.Add(time.Duration(effective)*time.Millisecond))
		}
		if !compact && tree.selectWaitOutcome(wait) {
			wait.State = "completed"
		} else if !compact && wait.EffectiveMS != nil && *wait.EffectiveMS == 0 {
			wait.State = "timeout"
		}
		member.Wait = wait
		result = *tree.waitResult(wait)
		return nil
	})
	return
}

// Execution records authenticated enclosing-cell evidence, never the MCP result.
// An empty handle is terminal: success arms the stop; failure retires the wait.
func (s *Service) Execution(ctx context.Context, thread, turn, item, handle string, succeeded bool) error {
	if handle != "" && (!strings.HasPrefix(handle, "cell:") || len(handle) == len("cell:")) {
		return errors.New("wait requires a host execution-cell handle")
	}
	return s.update(ctx, thread, func(tree *Tree, member *Member) error {
		_, err := tree.transitionHost(member, hostEvent{kind: hostEventExecution, turn: turn, item: item, handle: handle, succeeded: succeeded})
		return err
	})
}

func (s *Service) ClaimLocalStop(ctx context.Context, thread, turn, item string) (result *LocalStop, err error) {
	err = s.update(ctx, thread, func(tree *Tree, member *Member) error {
		var transition error
		result, transition = tree.transitionHost(member, hostEvent{kind: hostEventLocalStop, turn: turn, item: item})
		return transition
	})
	return
}

func (s *Service) PrepareWake(ctx context.Context, thread string, now time.Time) (result *Effect, err error) {
	err = s.update(ctx, thread, func(tree *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.State != "registered" || wait.Stop != "committed" || member.HostRunning || member.Status != Waiting || wait.Generation != member.Generation {
			return unchanged
		}
		if wait.CompactionPending() {
			return unchanged
		}
		if !wait.Standalone && !tree.selectWaitOutcome(wait) && (wait.Deadline == nil || now.Before(*wait.Deadline)) {
			return unchanged
		}
		wait.State = "queued"
		result = new(wait.Effect)
		return nil
	})
	return
}

func cancelSubscription(tree *Tree, member *Member) {
	wait := member.Wait
	if wait == nil {
		return
	}
	if wait.State == "queued" {
		for i := range tree.Effects {
			if tree.Effects[i].Target == member.Thread && tree.Effects[i].ClaimedBy == wait.ID {
				tree.Effects[i].ClaimedBy = ""
			}
		}
	}
	if wait.Compaction != nil && !hostOwned(wait.Compaction.State) {
		wait.Compaction.State = "canceled"
	}
	wait.State, wait.Stop = "canceled", ""
}

func (tree *Tree) wakeResult(wait *Wait) *WaitResult {
	result := tree.waitResult(wait)
	result.State = "timeout"
	if wait.Standalone || result.Event != nil || len(result.Events) != 0 {
		result.State = "completed"
	}
	return result
}

// WaitPublication reads the selected subscription and its actual wake reason.
// It borrows the immutable tree and copies only detached display facts.
func (s *Service) WaitPublication(thread string) *WaitResult {
	owner, err := s.owner(thread)
	if err != nil {
		return nil
	}
	tree := owner.state.Load()
	member := tree.Members[thread]
	wait := member.Wait
	if member.RecoveryWait != nil {
		wait = member.RecoveryWait
	}
	if wait == nil {
		return nil
	}
	result := tree.waitResult(wait)
	result.Metrics = nil // Display facts do not borrow frozen continuation payloads.
	result.EffectiveMS = nil
	if wait.EffectiveMS != nil {
		result.EffectiveMS = new(*wait.EffectiveMS)
	}
	if !wait.Standalone && wait.State == "completed" && result.Event == nil && len(result.Events) == 0 {
		result.State = "timeout"
	}
	return result
}

// CompactionPending holds deliveries from checkpoint registration through settlement.
func (wait *Wait) CompactionPending() bool {
	return wait != nil && wait.Compaction != nil && (hostOwned(wait.Compaction.State) || wait.Compaction.State == "registered" && wait.State == "registered" || wait.Stop == "committed" && wait.Compaction.State != "completed" && wait.Compaction.State != "failed" && wait.Compaction.State != "canceled")
}
