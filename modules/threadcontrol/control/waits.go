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
		return member, nil
	}
	candidate := target
	if !strings.HasPrefix(candidate, "/") {
		candidate = caller.Task + "/" + candidate
	}
	for _, member := range tree.Members {
		if member.Task == candidate {
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
		if effect.delivered() && effect.Target == wait.Caller && effect.ClaimedBy == "" && slices.Contains(wait.Targets, effect.Caller) {
			return effect
		}
	}
	return nil
}

func (tree *Tree) waitResult(wait *Wait) *WaitResult {
	result := &WaitResult{ID: wait.ID, State: wait.State}
	for _, effect := range tree.Effects {
		if effect.delivered() && effect.ClaimedBy == wait.ID && effect.Target == wait.Caller {
			result.Event = tree.event(effect)
			break
		}
	}
	return result
}

func (s *Service) RegisterWait(ctx context.Context, caller Identity, targets []string, all bool, timeout *int64, now time.Time) (result WaitResult, err error) {
	err = s.update(ctx, caller.Thread, func(tree *Tree, member *Member) error {
		if _, err := admitted(tree, caller); err != nil {
			return err
		}
		if caller.Call == "" || caller.Item == "" {
			return errors.New("wait requires enclosing host-call identity")
		}
		selected, err := descendants(tree, member, targets, all)
		if err != nil {
			return err
		}
		if old := member.Wait; old != nil && (old.ID == caller.Call || old.State != "canceled" && old.State != "completed" && old.State != "paused") {
			result = *tree.waitResult(old)
			return unchanged
		}
		wait := &Wait{Effect: Effect{ID: caller.Call, Caller: caller.Thread, Target: caller.Thread, Generation: member.Generation, Turn: caller.Turn, CallerTurn: caller.Turn, Kind: "wake", State: "registered"}, Item: caller.Item, Targets: selected}
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
		if event := selectedEvent(tree, wait); event != nil {
			event.ClaimedBy, wait.State = wait.ID, "completed"
		} else if wait.EffectiveMS != nil && *wait.EffectiveMS == 0 {
			wait.State = "timeout"
		}
		member.Wait = wait
		result = *tree.waitResult(wait)
		return nil
	})
	return
}

func (s *Service) CancelWait(ctx context.Context, caller Identity, id string) (result WaitResult, err error) {
	err = s.update(ctx, caller.Thread, func(tree *Tree, member *Member) error {
		if _, err := admitted(tree, caller); err != nil {
			return err
		}
		if member.Wait == nil || member.Wait.ID != id {
			return errors.New("unknown caller wait")
		}
		if hostOwned(member.Wait.State) {
			return errors.New("wait continuation is host-owned and requires reconciliation")
		}
		cancelSubscription(tree, member)
		member.Status = Working
		result = WaitResult{ID: id, State: "canceled"}
		return nil
	})
	return
}

// Execution records authenticated enclosing-cell evidence, never the MCP result.
func (s *Service) Execution(ctx context.Context, thread, turn, item, handle string, completed bool) error {
	if handle != "" && (!strings.HasPrefix(handle, "cell:") || len(handle) == len("cell:")) {
		return errors.New("wait requires a host execution-cell handle")
	}
	return s.update(ctx, thread, func(_ *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.Turn != turn || wait.Item != item || wait.Generation != member.Generation || wait.State != "registered" || wait.Stop == "committed" {
			return unchanged
		}
		if handle != "" {
			if wait.ResumeHandle != "" && wait.ResumeHandle != handle {
				return errors.New("conflicting enclosing-cell handle")
			}
			wait.ResumeHandle = handle
		}
		if completed {
			wait.Stop = "eligible"
		}
		return nil
	})
}

func (s *Service) ClaimLocalStop(ctx context.Context, thread, turn, item string) (result *LocalStop, err error) {
	err = s.update(ctx, thread, func(_ *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.Stop != "eligible" && wait.Stop != "committed" || wait.State != "registered" || wait.Turn != turn || item != "" && wait.Item != item || wait.Generation != member.Generation || !member.HostRunning {
			return unchanged
		}
		if wait.Stop == "committed" {
			result = new(*member.Stop)
			return unchanged
		}
		wait.Stop, member.Status = "committed", Waiting
		member.Stop = &LocalStop{Generation: member.Generation, Turn: turn, Item: wait.Item, Wait: wait.ID}
		result = new(*member.Stop)
		return nil
	})
	return
}

func (s *Service) PrepareWake(ctx context.Context, thread string, now time.Time) (result *Effect, err error) {
	err = s.update(ctx, thread, func(tree *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.State != "registered" || wait.Stop != "committed" || member.HostRunning || member.Status != Waiting || wait.Generation != member.Generation {
			return unchanged
		}
		event := selectedEvent(tree, wait)
		if event == nil && (wait.Deadline == nil || now.Before(*wait.Deadline)) {
			return unchanged
		}
		if event != nil {
			event.ClaimedBy = wait.ID
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
	wait.State, wait.Stop = "canceled", ""
}

func (tree *Tree) wakeResult(wait *Wait) *WaitResult {
	result := tree.waitResult(wait)
	result.State = "timeout"
	if result.Event != nil {
		result.State = "completed"
	}
	return result
}
