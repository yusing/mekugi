package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v7"
	compactreload "github.com/yusing/mekugi/modules/agentguidance/compact"
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

func descendants(tree *Tree, caller *Member) ([]string, error) {
	unread := make(map[string]bool)
	message := false
	for _, effect := range tree.Effects {
		if effect.Target != caller.Thread || effect.Consumed || effect.ClaimedBy != "" || !(effect.delivered() || effect.State == "pending" || hostOwned(effect.State) || effect.State == "paused") {
			continue
		}
		if effect.Kind == "final" {
			unread[effect.source()] = true
		} else if effect.Kind == "message" {
			message = true
		}
	}
	selected := []string{}
	for id, member := range tree.Members {
		if strings.HasPrefix(member.Task, caller.Task+"/") && (member.Reserved() || member.HostRunning || unread[id]) {
			selected = append(selected, id)
		}
	}
	slices.Sort(selected)
	if len(selected) == 0 && !message {
		return nil, errors.New("wait selection is empty")
	}
	return selected, nil
}

func selectedEvent(tree *Tree, wait *Wait) *Effect {
	for i := range tree.Effects {
		effect := &tree.Effects[i]
		if effect.delivered() && !effect.Consumed && effect.Target == wait.Caller && effect.ClaimedBy == "" && (effect.Kind == "message" || slices.Contains(wait.Targets, effect.source())) {
			return effect
		}
	}
	return nil
}

func (tree *Tree) waitResult(wait *Wait) *WaitResult {
	result := &WaitResult{Progress: wait.Progress, ID: wait.ID, Standalone: wait.Standalone, UsageLimit: wait.UsageLimit, Capacity: wait.Capacity, Mode: wait.Mode, Events: slices.Clone(wait.Terminals), State: wait.PublicState(), EffectiveMS: wait.EffectiveMS, Metrics: wait.Metrics}
	result.Timed = wait.Deadline != nil
	if wait.UsageLimit && wait.Deadline != nil {
		result.Deadline = new(*wait.Deadline)
	}
	if wait.Standalone || wait.UsageLimit {
		result.Mode = ""
	}
	if compact := wait.Compaction; compact != nil {
		if wait.NativeCell != "" {
			result.NativeTicket = &CompoundTicket{WaitID: wait.ID, Generation: wait.Generation}
		}
		result.Compaction = &CompactionResult{State: compact.State, Error: compact.Error, Reload: compact.Message}
		if compact.State == "registered" {
			result.State = "compacting"
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

func (wait *WaitResult) Settled() bool {
	return wait != nil && (wait.State == "completed" || wait.State == "timeout" || wait.State == "canceled" || wait.State == "paused" || wait.State == "failed" || wait.State == "yielded")
}

// StatusText is shared by ordinary and temporary conversation publications.
func (wait *WaitResult) StatusText() string {
	retry := ""
	if wait.Deadline != nil {
		retry = " · retry at " + wait.Deadline.Local().Format("15:04:05 MST Jan 2")
	}
	if wait.Capacity {
		return "Waiting for provider capacity" + retry
	}
	if !wait.UsageLimit {
		return "Waiting for agents"
	}
	if !wait.Timed {
		return "Usage limit · paused"
	}
	return "Waiting for usage limit reset" + retry
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
		wait.Terminal = tree.stoppedEvent(wait, member)
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
		if effect.Target != wait.Caller {
			continue
		}
		if effect.Kind == "message" && effect.delivered() && !effect.Consumed && effect.ClaimedBy == "" {
			effect.ClaimedBy = wait.ID
			return true
		}
		if !selected[effect.source()] {
			continue
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
	if len(wait.Targets) == 0 {
		return false // An addressed message may still be awaiting delivery.
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
			wait.Terminals = append(wait.Terminals, *tree.stoppedEvent(wait, member))
		}
	}
	return true
}

func (s *Service) RegisterWait(ctx context.Context, caller Identity, mode WaitMode, timeout, yield *int64, now time.Time) (result WaitResult, err error) {
	return s.registerWait(ctx, caller, mode, timeout, yield, now, false, false, "")
}

// RegisterCompaction registers the checkpoint and optional wait in one transaction.
func (s *Service) RegisterCompaction(ctx context.Context, caller Identity, mode WaitMode, timeout, yield *int64, now time.Time, standalone bool, reload string) (WaitResult, error) {
	return s.registerWait(ctx, caller, mode, timeout, yield, now, true, standalone, reload)
}

func (s *Service) registerWait(ctx context.Context, caller Identity, mode WaitMode, timeout, yield *int64, now time.Time, compact, standalone bool, reload string) (result WaitResult, err error) {
	if mode == "" {
		mode = WaitAny
	}
	if mode != WaitAny && mode != WaitAll {
		return result, errors.New("wait_mode must be any or all")
	}
	if yield != nil && (*yield < int64((5*time.Minute)/time.Millisecond) || *yield > int64((1<<63-1)/int64(time.Millisecond))) {
		return result, errors.New("yield_time_ms must be at least 300000 and fit a duration")
	}
	registered := false
	defer func() {
		// Repeated calls from the same enclosing execution reuse the wait.
		if err == nil && registered {
			if compact {
				s.ObserveFeature(caller.Thread, FeatureSelfCompaction)
				if sources, _ := compactreload.ReloadSources(reload); len(sources) > 0 {
					s.ObserveFeature(caller.Thread, FeatureCompactionPreload)
				}
			}
			if !standalone {
				s.ObserveFeature(caller.Thread, FeatureWait)
			}
			if !standalone && mode == WaitAll {
				s.ObserveFeature(caller.Thread, FeatureWaitAll)
			}
			if !standalone && yield != nil {
				s.ObserveFeature(caller.Thread, FeatureWaitProgress)
			}
		}
	}()
	err = s.update(ctx, caller.Thread, func(tree *Tree, member *Member) error {
		if _, err := admitted(tree, caller); err != nil {
			return err
		}
		if caller.Call == "" || caller.Item == "" {
			return errors.New("wait requires enclosing host-call identity")
		}
		if _, err := tree.transitionHost(member, hostEvent{kind: hostEventWaitRegistration, turn: caller.Turn, item: caller.Item, call: caller.Call, compact: compact, standalone: standalone}); err != nil {
			if err == unchanged {
				result = *tree.waitResult(member.Wait)
			}
			return err
		}
		var selected []string
		if !standalone {
			var err error
			selected, err = descendants(tree, member)
			if err != nil {
				return err
			}
		}
		wait := &Wait{ID: caller.Call, Caller: caller.Thread, Target: caller.Thread, Generation: member.Generation, Turn: caller.Turn, CallerTurn: caller.Turn, Kind: "wake", State: "registered", Item: caller.Item, Targets: selected, Mode: mode}
		wait.Standalone = standalone
		if yield != nil {
			wait.YieldMS = *yield
			wait.YieldAt = new(now.Add(time.Duration(*yield) * time.Millisecond))
		}
		if compact {
			wait.Compaction = &Effect{ID: caller.Call + ":compact", Caller: caller.Thread, Target: caller.Thread, CallerTurn: caller.Turn, Generation: member.Generation, Kind: "compact", State: "registered", Message: reload}
			wait.NativeCell, wait.Window = caller.Cell, caller.Window
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
		registered = true
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
		if member.Wait != nil && member.Wait.NativeCell != "" {
			return unchanged // Native compounds settle from typed cell evidence only.
		}
		_, err := tree.transitionHost(member, hostEvent{kind: hostEventExecution, turn: turn, item: item, handle: handle, succeeded: succeeded})
		return err
	})
}

func (s *Service) ClaimLocalStop(ctx context.Context, thread, turn, item string) (result *LocalStop, err error) {
	err = s.update(ctx, thread, func(tree *Tree, member *Member) error {
		if member.Wait != nil && member.Wait.NativeCell != "" {
			return unchanged
		}
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
		if wait.UsageLimit && (wait.Deadline == nil || now.Before(*wait.Deadline)) {
			return unchanged
		}
		if !wait.Standalone && !tree.selectWaitOutcome(wait) && (wait.Deadline == nil || now.Before(*wait.Deadline)) {
			if len(wait.Summaries) != 0 {
				tree.resolveStoppedSummaries(wait)
				if !wait.summariesReady() {
					return nil
				}
			} else if wait.YieldAt == nil || now.Before(*wait.YieldAt) {
				return unchanged
			}
			wait.Yielded = true
		} else {
			wait.Yielded, wait.Progress, wait.YieldSeen, wait.Summaries = false, nil, nil, nil
		}
		wait.State = "queued"
		result = new(wait.Effect)
		return nil
	})
	return
}

// WaitForUsageLimit checkpoints rejected inference through the ordinary logical
// stop. Only provider timing can release it; human input and interruption retain
// their existing supersession rules.
func (s *Service) WaitForUsageLimit(ctx context.Context, thread, turn string, deadline *time.Time) (result *LocalStop, err error) {
	return s.waitForProvider(ctx, thread, turn, deadline, false)
}

// WaitForCapacity uses provider timing or a work-scoped exponential fallback.
func (s *Service) WaitForCapacity(ctx context.Context, thread, turn string, deadline *time.Time) (*LocalStop, error) {
	return s.waitForProvider(ctx, thread, turn, deadline, true)
}

// ProviderRecovered resets fallback timing only for the admitted running turn.
func (s *Service) ProviderRecovered(ctx context.Context, thread, turn string) error {
	if member, ok := s.member(thread); !ok || member.CapacityRetrySeconds == 0 {
		return nil
	}
	return s.update(ctx, thread, func(_ *Tree, member *Member) error {
		if !member.HostRunning || member.Turn != turn || member.CapacityRetrySeconds == 0 {
			return unchanged
		}
		member.CapacityRetrySeconds = 0
		return nil
	})
}

func (s *Service) waitForProvider(ctx context.Context, thread, turn string, deadline *time.Time, capacity bool) (result *LocalStop, err error) {
	err = s.update(ctx, thread, func(tree *Tree, member *Member) error {
		if !member.HostRunning || member.Turn != turn {
			return errors.New("usage limit does not belong to the running host turn")
		}
		id := fmt.Sprintf("%s:%d:%s:usage-limit", thread, member.Generation, turn)
		if member.Stop != nil && member.Stop.Wait == id {
			result = new(*member.Stop)
			return unchanged
		}
		if capacity && deadline == nil {
			policy := &backoff.ExponentialBackOff{InitialInterval: time.Second, Multiplier: 2, MaxInterval: 30 * time.Second}
			if member.CapacityRetryGeneration == member.Generation && member.CapacityRetrySeconds != 0 {
				policy.InitialInterval = time.Duration(member.CapacityRetrySeconds) * time.Second
				policy.NextBackOff() // Advance past the preceding fallback interval.
			}
			member.CapacityRetrySeconds = int64(policy.NextBackOff() / time.Second)
			member.CapacityRetryGeneration = member.Generation
			deadline = new(time.Now().Add(time.Duration(member.CapacityRetrySeconds) * time.Second))
		}
		wait := &Wait{Effect: Effect{ID: id, Caller: thread, Target: thread, Generation: member.Generation, Turn: turn, CallerTurn: turn, Kind: "wake", State: "registered"}, Stop: "committed", UsageLimit: true, Capacity: capacity, Deadline: deadline}
		if member.Wait != nil && hostOwned(member.Wait.State) {
			member.RecoveryWait = wait
		} else {
			cancelSubscription(tree, member)
			member.Wait = wait
		}
		member.Stop = &LocalStop{Generation: member.Generation, Turn: turn, Wait: id}
		member.Status = Waiting
		result = new(*member.Stop)
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
	if compact := wait.Compaction; compact != nil {
		switch compact.State {
		case "registered", "pending", "paused":
			compact.State = "canceled"
		}
	}
	wait.State, wait.Stop = "canceled", ""
}

func (tree *Tree) wakeResult(wait *Wait) *WaitResult {
	result := tree.waitResult(wait)
	result.State = "timeout"
	if wait.Yielded {
		result.State = "yielded"
	}
	if wait.Standalone || wait.UsageLimit || result.Event != nil || len(result.Events) != 0 {
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
	result.Progress = nil
	result.Metrics = nil // Display facts do not borrow frozen continuation payloads.
	result.Turn, result.Stop = wait.Turn, wait.Stop
	result.EffectiveMS = nil
	if wait.EffectiveMS != nil {
		result.EffectiveMS = new(*wait.EffectiveMS)
	}
	if !wait.Standalone && wait.State == "completed" && result.Event == nil && len(result.Events) == 0 {
		result.State = "timeout"
		if wait.Yielded {
			result.State = "yielded"
		}
	}
	return result
}

// CompactionPending holds deliveries from checkpoint registration through settlement.
func (wait *Wait) CompactionPending() bool {
	return wait != nil && wait.Compaction != nil && (hostOwned(wait.Compaction.State) || wait.Compaction.State == "registered" && wait.State == "registered" || wait.Stop == "committed" && wait.Compaction.State != "completed" && wait.Compaction.State != "failed" && wait.Compaction.State != "skipped" && wait.Compaction.State != "canceled")
}
