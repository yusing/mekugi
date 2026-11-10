package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ProgressReplyPrefix uses the existing message route, but never publishes a
// message event. The authenticated sender and retained slot grant authority;
// the model-supplied token alone does not.
const ProgressReplyPrefix = "MEKUGI_PROGRESS:"

func summaryRequest(selection, recipient, id string) string {
	route := fmt.Sprintf("send_message(target=%q, message=%q + your summary)", AgentName(recipient), ProgressReplyPrefix+id+"\n")
	return fmt.Sprintf("Progress checkpoint, not a new assignment. Summarize only %s for your coordinator: consequential changes and rationale, assumptions/deviations/risks, observed validation and blockers. Use existing context and completed tool results only; do no summary-only reads, edits, tests or investigation. State missing evidence. Aim for two or three sentences, about 256 tokens; longer replies are valid. Reply nonterminally via %s, not a final answer or ordinary message, then continue your assignment.", selection, route)
}

// BeginWaitSummaries freezes precisely one batch inside the existing wait. It
// releases the wake claim so deadlines and ordinary outcomes can supersede it.
func (s *Service) BeginWaitSummaries(ctx context.Context, thread, id, contextID string, progress WaitProgress, seen map[string]int) error {
	frozen, err := detach(struct {
		Progress WaitProgress
		Seen     map[string]int
	}{progress, seen})
	if err != nil {
		return err
	}
	return s.update(ctx, thread, func(tree *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.ID != id || wait.State != "dispatching" || !wait.Yielded || wait.Progress != nil {
			return errors.New("summary collection requires the unstarted yield")
		}
		wait.Progress, wait.YieldSeen, wait.YieldContext = &frozen.Progress, frozen.Seen, contextID
		wait.State, wait.YieldAt = "registered", nil
		for i, agent := range frozen.Progress.Agents {
			var target *Member
			for _, thread := range wait.Targets {
				if candidate := tree.Members[thread]; candidate != nil && candidate.Task == agent.Task {
					target = candidate
					break
				}
			}
			if target == nil {
				return errors.New("progress agent is outside the selected wait")
			}
			effect := Effect{ID: fmt.Sprintf("%s:summary:%d", id, i), Caller: thread, CallerTurn: wait.Turn, CallerGeneration: member.Generation, Target: target.Thread, Generation: target.Generation, Turn: target.Turn, Kind: "summary", State: "pending"}
			effect.Message = summaryRequest(agent.Changes, member.Task, effect.ID)
			wait.Summaries = append(wait.Summaries, effect)
		}
		tree.resolveStoppedSummaries(wait)
		return nil
	})
}

func (wait *Wait) summariesReady() bool {
	if len(wait.Summaries) == 0 {
		return false
	}
	for _, effect := range wait.Summaries {
		if effect.State != "completed" && effect.State != "failed" {
			return false
		}
	}
	return true
}

func (wait *Wait) summaryUnavailable(index int, reason string) {
	wait.Summaries[index].State, wait.Summaries[index].Error = "failed", reason
	wait.Progress.Agents[index].Summary = "Summary unavailable: " + reason
}

// ToolBoundarySummaries supplies native post-tool-batch context for backends
// without an active-turn-preconditioned steer. It consumes no logical work or
// completion notification and never queues input that could start another turn.
func (s *Service) ToolBoundarySummaries(ctx context.Context, caller Identity) (text string, err error) {
	err = s.update(ctx, caller.Thread, func(tree *Tree, member *Member) error {
		if _, err := admitted(tree, caller); err != nil {
			return err
		}
		var messages []string
		for id, receiver := range tree.Members {
			wait := receiver.Wait
			if wait == nil || wait.State != "registered" || !wait.Yielded {
				continue
			}
			for i, slot := range wait.Summaries {
				if slot.Target != caller.Thread || slot.Generation != member.Generation || slot.Turn != caller.Turn || slot.Consumed || slot.State != "confirmed" && slot.State != "dispatching" {
					continue
				}
				wait = tree.edit(id).Wait
				wait.Summaries[i].Consumed = true
				messages = append(messages, slot.Message)
			}
		}
		if len(messages) == 0 {
			return unchanged
		}
		text = strings.Join(messages, "\n\n")
		return nil
	})
	return
}

func (s *Service) AcknowledgeSummary(ctx context.Context, caller, id string, receipt Receipt) error {
	return s.update(ctx, caller, func(_ *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.State != "registered" || !wait.Yielded {
			return unchanged
		}
		for i := range wait.Summaries {
			slot := &wait.Summaries[i]
			if slot.ID != id || slot.State != "dispatching" {
				continue
			}
			if receipt.Uncertain || receipt.Error != "" {
				reason := receipt.Error
				if receipt.Uncertain {
					reason = "checkpoint request delivery uncertain: " + reason
				}
				wait.summaryUnavailable(i, reason)
			} else {
				slot.State = "confirmed"
			}
			return nil
		}
		return unchanged
	})
}

func (tree *Tree) resolveStoppedSummaries(wait *Wait) {
	for i, slot := range wait.Summaries {
		if slot.State == "completed" || slot.State == "failed" {
			continue
		}
		member := tree.Members[slot.Target]
		if member == nil {
			wait.summaryUnavailable(i, "agent is no longer admitted")
			continue
		}
		if member.Generation != slot.Generation {
			wait.summaryUnavailable(i, "agent's work generation changed")
			continue
		}
		if member.HostRunning && member.Turn == slot.Turn {
			continue
		}
		reason := fmt.Sprintf("agent is %s; checkpoint host turn is no longer running", member.Status)
		for _, effect := range tree.Effects {
			if effect.Kind == "final" && effect.Caller == member.Thread && effect.CallerGeneration == slot.Generation && effect.Outcome != Completed && effect.Message != "" {
				reason += ": " + effect.Message
				break
			}
		}
		wait.summaryUnavailable(i, reason)
	}
}

// ProgressReply consumes only a checkpoint envelope. Stale/duplicate envelopes
// are acknowledged without reviving a wait or becoming an ordinary message.
func (s *Service) ProgressReply(ctx context.Context, caller Identity, target, message string) (recipient string, accepted bool, err error) {
	header, summary, found := strings.Cut(message, "\n")
	id := strings.TrimPrefix(header, ProgressReplyPrefix)
	if !found || id == header || strings.TrimSpace(summary) == "" {
		return "", false, errors.New("invalid progress checkpoint reply")
	}
	err = s.update(ctx, caller.Thread, func(tree *Tree, member *Member) error {
		if _, err := admitted(tree, caller); err != nil {
			return err
		}
		receiver, err := resolve(tree, member, target)
		if err != nil {
			return err
		}
		receiver = tree.edit(receiver.Thread)
		recipient = receiver.Thread
		wait := receiver.Wait
		if wait == nil || wait.State != "registered" || !wait.Yielded {
			return unchanged
		}
		for i := range wait.Summaries {
			slot := &wait.Summaries[i]
			if slot.ID != id || slot.Target != caller.Thread || slot.Generation != member.Generation || slot.Turn != caller.Turn {
				continue
			}
			if slot.State != "dispatching" && slot.State != "confirmed" {
				return unchanged
			}
			slot.State = "completed"
			wait.Progress.Agents[i].Summary = summary
			accepted = true
			return nil
		}
		return unchanged
	})
	return
}

// RetainProgressOutput freezes the transport representation, including recovery,
// before delivery. Omitted entries remain unacknowledged capture evidence.
func (s *Service) RetainProgressOutput(ctx context.Context, thread, id string, progress WaitProgress) error {
	frozen, err := detach(progress)
	if err != nil {
		return err
	}
	return s.update(ctx, thread, func(_ *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.ID != id || wait.State != "dispatching" || !wait.Yielded {
			return errors.New("progress output requires the claimed yield")
		}
		wait.Progress = &frozen
		if progress.Recovery != "" {
			wait.YieldSeen = nil
		}
		return nil
	})
}
