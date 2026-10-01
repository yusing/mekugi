package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
)

// journalResetDriver is the shared app-server policy. It observes host events
// and sends native RPCs; neither the terminal nor a headless adapter owns a
// second continuation or compaction policy.
type journalResetDriver struct {
	ctx                                   context.Context
	proxy                                 *mekugiProxy
	client                                *appserver.Client
	workspace, thread                     string
	intent                                *journalResetIntent
	phase, requestID                      string
	deadline                              time.Time
	delay                                 time.Duration
	compactTurn                           string
	compactAck, compactDone, compactEnded bool
	startPending                          bool
	continuationTurn, lastStartedTurn     string
	cancelled                             bool
	notice                                string
	pendingCompleted                      string
}

func (d *journalResetDriver) active() bool { return d != nil && (d.phase != "" || d.startPending) }

func (d *journalResetDriver) send(phase, method string, params any) error {
	id, err := d.client.Send(method, params, true)
	if err != nil {
		d.startPending = false
		d.phase, d.requestID = "", ""
		d.notice = "Slice request outcome unknown; not retried."
		if d.intent != nil {
			// Keep the evidence, but an armed intent must not answer a later,
			// unrelated manual compaction.
			_ = d.change(func(_ *threadJournal, intent *journalResetIntent) error {
				if intent.Phase == "armed" {
					intent.Phase = "unknown"
				}
				return nil
			})
		}
		return err
	}
	d.phase, d.requestID = phase, id
	return nil
}

func (d *journalResetDriver) change(mutate func(*threadJournal, *journalResetIntent) error) error {
	return d.proxy.journals.changeReset(d.ctx, d.proxy.replayStore, d.workspace, d.thread, d.intent.ID, mutate)
}

func (d *journalResetDriver) completed(turn string) error {
	if d.active() {
		if turn != "" && turn == d.continuationTurn {
			d.startPending = false
			d.pendingCompleted = turn
		} else if d.phase == "starting" && d.continuationTurn == "" {
			d.pendingCompleted = turn // Validate against the turn/start reply.
		}
		if d.phase == "" && !d.startPending {
			return d.finish()
		}
		return nil
	}
	intent, err := d.proxy.journals.completedSlice(d.ctx, d.proxy.replayStore, d.workspace, d.thread, turn)
	if err != nil || intent == nil {
		return err
	}
	d.intent, d.cancelled, d.notice = intent, false, ""
	return d.countdown(time.Now())
}

// Only a not-yet-dispatched countdown that still matches the journal is
// replayable. An interrupted process never repeats an RPC whose acceptance is
// unknown; it reports and discards that intent.
func (d *journalResetDriver) restore() error {
	intent, interrupted, err := d.proxy.journals.restorableReset(d.ctx, d.proxy.replayStore, d.workspace, d.thread)
	if interrupted {
		d.notice = "Slice continuation was interrupted; continue the plan manually."
	}
	if err != nil || intent == nil {
		return err
	}
	d.intent = intent
	return d.countdown(time.Now())
}

func (d *journalResetDriver) countdown(now time.Time) error {
	if d.cancelled {
		return d.finish()
	}
	d.phase, d.deadline = "countdown", now.Add(d.delay)
	return nil
}

// cancellable reports whether nothing context-changing has been dispatched yet.
func (d *journalResetDriver) cancellable() bool {
	return d.active() && d.phase == "countdown"
}

func (d *journalResetDriver) cancel() error {
	if !d.cancellable() || d.cancelled {
		return nil
	}
	d.cancelled, d.notice = true, "Slice continuation cancelled"
	if err := d.change(func(_ *threadJournal, intent *journalResetIntent) error { intent.Phase = "cancelled"; return nil }); err != nil {
		return err
	}
	return d.finish()
}

func (d *journalResetDriver) finish() error {
	if d.intent != nil {
		if err := d.change(func(j *threadJournal, _ *journalResetIntent) error { j.ResetIntent = nil; return nil }); err != nil {
			d.notice += "; " + err.Error()
		}
		d.intent = nil
	}
	d.phase, d.requestID = "", ""
	if d.pendingCompleted != "" && !d.startPending {
		turn := d.pendingCompleted
		d.pendingCompleted = ""
		return d.completed(turn)
	}
	return nil
}

func (d *journalResetDriver) fail(message string) error {
	d.startPending = false
	d.notice = message
	if d.intent != nil {
		if err := d.change(func(_ *threadJournal, intent *journalResetIntent) error { intent.Phase = "failed"; return nil }); err != nil {
			d.notice += "; " + err.Error()
		}
	}
	return d.finish()
}

func (d *journalResetDriver) tick(now time.Time) error {
	if d == nil || d.phase != "countdown" || now.Before(d.deadline) {
		return nil
	}
	if d.proxy.journalCompaction == "off" || d.proxy.journalCompaction == "" {
		return d.continuePlan(false)
	}
	if err := d.change(func(j *threadJournal, intent *journalResetIntent) error {
		i := j.treeIndex(intent.Path)
		if i < 0 || j.Items[i].State != "pending" {
			return errors.New("next slice is no longer pending")
		}
		intent.Phase = "armed"
		return nil
	}); err != nil {
		return d.fail("Slice reset unavailable: " + err.Error())
	}
	d.compactAck, d.compactDone, d.compactEnded, d.compactTurn = false, false, false, ""
	return d.send("compacting", "thread/compact/start", map[string]any{"threadId": d.thread})
}

func (d *journalResetDriver) continuePlan(compacted bool) error {
	if err := d.change(func(j *threadJournal, intent *journalResetIntent) error {
		i := j.treeIndex(intent.Path)
		if i < 0 || j.Items[i].State != "pending" {
			return errors.New("next slice is no longer pending")
		}
		if compacted && intent.Phase != "consumed" {
			return errors.New("context reset did not consume the journal reset intent")
		}
		intent.Phase = "starting"
		if compacted {
			_, err := j.applyTree(journalMutation{Op: "log", P: intent.Path, Text: new("↻ Context reset from journal · 0 provider tokens")})
			if err != nil {
				return err
			}
			j.Events[len(j.Events)-1].Op = "reset"
			j.Events[len(j.Events)-1].ResetTurn = d.compactTurn
		}
		return nil
	}); err != nil {
		return d.fail("Slice continuation unavailable: " + err.Error())
	}
	d.startPending = true
	d.continuationTurn, d.lastStartedTurn, d.pendingCompleted = "", "", ""
	return d.send("starting", "turn/start", map[string]any{
		"threadId": d.thread, "clientUserMessageId": journalContinuationPrefix + d.intent.ID,
		"input": []any{map[string]any{"type": "text", "text": journalContinuationText(d.intent)}},
	})
}

func (d *journalResetDriver) maybeContinue() error {
	if d.phase == "compacting" && d.compactAck && d.compactDone && d.compactEnded {
		return d.continuePlan(true)
	}
	return nil
}

// message consumes only this driver's RPC acknowledgements. Lifecycle events
// are observed, never swallowed, so the normal UI/host turn state remains true.
func (d *journalResetDriver) message(m appserver.Message) (bool, error) {
	if d == nil {
		return false, nil
	}
	if m.Method == "" && d.requestID != "" && string(m.ID) == d.requestID {
		d.requestID = ""
		phase := d.phase
		if m.Error != nil {
			return true, d.fail("Slice continuation: " + m.Error.Message)
		}
		switch phase {
		case "compacting":
			d.compactAck = true
			return true, d.maybeContinue()
		case "starting":
			var started struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(m.Result, &started) != nil || started.Turn.ID == "" {
				return true, d.fail("Slice continuation acknowledgement has no turn identity; not retried")
			}
			d.continuationTurn = started.Turn.ID
			if d.pendingCompleted != d.continuationTurn {
				d.pendingCompleted = ""
			}
			d.startPending = d.lastStartedTurn != d.continuationTurn && d.pendingCompleted == ""
			return true, d.finish()
		}
		return true, nil
	}
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
		Item appServerItem `json:"item"`
	}
	if m.Method != "turn/started" && m.Method != "turn/completed" && m.Method != "item/completed" {
		return false, nil
	}
	if json.Unmarshal(m.Params, &p) != nil || p.ThreadID != d.thread {
		return false, nil
	}
	if m.Method == "turn/started" {
		if p.Turn.ID == "" {
			return false, nil
		}
		d.lastStartedTurn = p.Turn.ID
		if d.startPending && p.Turn.ID == d.continuationTurn {
			d.startPending = false
		}
		if d.phase == "compacting" {
			if d.intent != nil && p.Turn.ID == d.intent.Turn {
				return false, nil // Late notification from the completed slice.
			}
			if d.compactTurn != "" && d.compactTurn != p.Turn.ID {
				return false, d.fail("Another turn started during context reset; continuation was not sent")
			}
			d.compactTurn = p.Turn.ID
		} else if d.phase == "countdown" {
			return false, d.cancel()
		}
	}
	if d.phase == "compacting" {
		if m.Method == "item/completed" && p.TurnID == d.compactTurn && p.Item.Type == "contextCompaction" {
			d.compactDone = true
		}
		if m.Method == "turn/completed" && p.Turn.ID == d.compactTurn {
			if p.Turn.Status != "completed" || !d.compactDone {
				return false, d.fail("Context reset did not complete; continuation was not sent")
			}
			d.compactEnded = true
		}
		return false, d.maybeContinue()
	}
	return false, nil
}

func (d *journalResetDriver) label(now time.Time) string {
	if !d.active() {
		return ""
	}
	if d.phase == "countdown" {
		verb := "Resetting context"
		if d.proxy.journalCompaction == "off" || d.proxy.journalCompaction == "" {
			verb = "Continuing plan"
		}
		return fmt.Sprintf("%s in %ds · starting %s %s · Esc cancels", verb, max(0, int(d.deadline.Sub(now).Seconds()+1)), d.intent.Path, d.intent.Title)
	}
	switch d.phase {
	case "compacting":
		return "Journal slice · Resetting context"
	case "starting":
		return "Journal slice · Continuing plan"
	}
	return ""
}
