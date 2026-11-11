package control

import (
	"context"
	"encoding/json/jsontext"
	"errors"
)

// CompoundTicket identifies the existing logical wait, not a second wait store.
type CompoundTicket struct {
	WaitID     string `json:"waitId"`
	Generation uint64 `json:"generation"`
}

type CompoundSettlement struct {
	Thread string         `json:"threadId"`
	Turn   string         `json:"turnId"`
	Item   string         `json:"itemId"`
	Window string         `json:"windowId"`
	Cell   string         `json:"cellId"`
	Ticket CompoundTicket `json:"ticket"`
	Result string         `json:"result"`
}

type CompoundDisposition struct {
	Action     string         `json:"action"`
	Ticket     CompoundTicket `json:"ticket"`
	Reload     jsontext.Value `json:"reload,omitempty"`
	Diagnostic string         `json:"diagnostic,omitempty"`
}

type CompoundReset struct {
	Thread     string         `json:"threadId"`
	Turn       string         `json:"turnId"`
	Item       string         `json:"itemId"`
	Window     string         `json:"windowId"`
	Cell       string         `json:"cellId"`
	Ticket     CompoundTicket `json:"ticket"`
	Reset      string         `json:"reset"`
	Diagnostic string         `json:"diagnostic,omitempty"`
}

// SettleCompound serializes exact native evidence, stop admission and native
// ownership with human supersession. No lock spans the subsequent native reset.
func (s *Service) SettleCompound(ctx context.Context, input CompoundSettlement) (result CompoundDisposition, err error) {
	result = CompoundDisposition{Action: "superseded", Ticket: input.Ticket}
	if input.Thread == "" || input.Turn == "" || input.Item == "" || input.Window == "" || input.Cell == "" || input.Ticket.WaitID == "" {
		return result, errors.New("incomplete native compound identity")
	}
	if input.Result != "yielded" && input.Result != "succeeded" && input.Result != "failed" && input.Result != "terminated" {
		return result, errors.New("unknown native execution disposition")
	}
	err = s.update(ctx, input.Thread, func(tree *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.Compaction == nil || wait.ID != input.Ticket.WaitID || wait.Generation != input.Ticket.Generation || member.Generation != wait.Generation || wait.Turn != input.Turn || wait.Item != input.Item || wait.Window != input.Window || wait.NativeCell != input.Cell || wait.State != "registered" || !member.HostRunning {
			return unchanged
		}
		if wait.Stop == "committed" {
			// An uncertain transport cannot authorize another reset.
			return errors.New("compound reset already committed; reconcile native settlement")
		}
		handle := ""
		if input.Result == "yielded" {
			handle = "cell:" + input.Cell
		}
		if _, err := tree.transitionHost(member, hostEvent{kind: hostEventExecution, turn: input.Turn, item: input.Item, handle: handle, succeeded: input.Result == "succeeded"}); err != nil {
			return err
		}
		switch input.Result {
		case "yielded":
			result.Action = "retain"
		case "failed", "terminated":
			result.Action = "retire"
		case "succeeded":
			stop, err := tree.transitionHost(member, hostEvent{kind: hostEventLocalStop, turn: input.Turn, item: input.Item})
			if err != nil {
				return err
			}
			if stop == nil {
				return unchanged
			}
			wait.Compaction.State = "dispatching"
			wait.Compaction.Turn = input.Turn
			result.Action, result.Reload = "commit", jsontext.Value(wait.Compaction.Message)
		}
		return nil
	})
	return
}

// CompoundResetCompleted is independent of host settlement. PrepareWake requires
// both the terminal reset receipt and the actual host turn end, in either order.
func (s *Service) CompoundResetCompleted(ctx context.Context, input CompoundReset) error {
	if input.Reset != "completed" && input.Reset != "failed" && input.Reset != "canceled" && input.Reset != "uncertain" {
		return errors.New("unknown native reset disposition")
	}
	return s.update(ctx, input.Thread, func(_ *Tree, member *Member) error {
		wait := member.Wait
		if wait == nil || wait.NativeCell == "" || wait.Compaction == nil || wait.Turn != input.Turn || wait.Item != input.Item || wait.Window != input.Window || wait.NativeCell != input.Cell || input.Ticket.WaitID != "" && (wait.ID != input.Ticket.WaitID || wait.Generation != input.Ticket.Generation) {
			return unchanged
		}
		uncertain := wait.Compaction.State == "uncertain"
		if input.Reset == "uncertain" && wait.State == "registered" && wait.Compaction.State == "registered" {
			wait.Stop = "committed"
			member.Stop = &LocalStop{Generation: wait.Generation, Turn: wait.Turn, Item: wait.Item, Wait: wait.ID}
			member.Status = Uncertain
		} else if wait.Compaction.State != "dispatching" && !(uncertain && input.Reset != "uncertain") {
			return unchanged
		}
		wait.Compaction.State, wait.Compaction.Error = input.Reset, input.Diagnostic
		if stop := member.Stop; uncertain && member.Status == Uncertain && wait.State == "registered" &&
			wait.Generation == member.Generation && wait.Stop == "committed" && stop != nil &&
			stop.Generation == wait.Generation && stop.Wait == wait.ID && stop.Turn == wait.Turn && stop.Item == wait.Item {
			member.Status = Waiting
		}
		return nil
	})
}
