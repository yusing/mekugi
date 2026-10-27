package launch

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Defer only successful logical completions. Other host facts, including
// interruption/failure and auxiliary turns, keep their ordinary presentation.
func deferLogicalCompletion(attached *attachedPresentation, live *sessioncontrol.Live, message appserver.Message, reduced <-chan error) (bool, error) {
	if reduced == nil || message.Method != "turn/completed" {
		return false, nil
	}
	p, err := binding.DecodeNotification(message.Method, message.Params)
	if err != nil {
		return false, err
	}
	if p.ThreadID != live.Thread || p.Turn.ID != live.Turn || p.Turn.Status != "completed" || live.AuxiliaryTurn(p.Turn.ID) {
		return false, nil
	}
	attached.threadInput.completions = append(attached.threadInput.completions, logicalCompletion{message, reduced})
	return true, nil
}

// Release received completions after the serialized owner has checkpointed each
// exact observation. A preceding turn/start can still be queued when admitted.
func (p *threadInput) flushCompletions(dispatch func(appserver.Message) error) error {
	if p == nil {
		return nil
	}
	for len(p.completions) > 0 {
		pending := p.completions[0]
		var failure error
		select {
		case failure = <-pending.reduced:
		default:
			return nil
		}
		p.completions[0] = logicalCompletion{}
		p.completions = p.completions[1:]
		// The logical owner's failure notice remains authoritative.
		if failure != nil {
			continue
		}
		if err := dispatch(pending.message); err != nil {
			return err
		}
	}
	p.completions = nil
	return nil
}

type logicalCompletion struct {
	message appserver.Message
	reduced <-chan error
}

// Logical publications never wait for checkpoint writes on the host event loop.
func syncLogicalWork(attached *attachedPresentation, live *sessioncontrol.Live, threads *threadExecution, now time.Time) {
	if attached.threadInput == nil {
		attached.threadInput = &threadInput{driver: threads.driver}
		attached.interruptWait = func(thread, wait string) {
			attached.threadInput.reset()
			threads.driver.Interrupt(thread, wait)
		}
	}
	member, ok := threads.shared.Member(live.Thread)
	attached.threadInput.admitted = ok && member.AudienceAdmitted
	attached.threadInput.caller, attached.threadInput.role = "", ""
	if ok {
		attached.threadInput.caller, attached.threadInput.role = member.Task, member.Settings.Role
	}
	if !ok {
		return
	}
	attached.threadInput.interruptions = threads.shared.Interruptions(live.Thread, attached.threadInput.interruptions)
	attached.source.SetInterruptions(attached.threadInput.interruptions)
	attached.childSource.SetInterruptions(attached.threadInput.interruptions)
	// A previously claimed wake already owns the sole continuation. Shell
	// execution gates its host dispatch, rather than creating a competing start.
	if member.Wait != nil && (member.Wait.State == "dispatching" || member.Wait.State == "uncertain" || member.Wait.State == "completed") {
		live.Shell.ConsumeContinuation()
	}
	stopped := live.LocalStop
	live.LocalStop, live.Waiting = "", ""
	// A continuation acknowledgement can precede its turn/started notification.
	// Keep the confirmed stop until presentation has observed that next turn.
	if stop := member.Stop; stop != nil && (stop.Turn == member.Turn || stop.Turn == live.Turn || live.Turn == "" && stop.Turn == stopped) && stop.Generation == member.Generation && (member.Status == threadcontrol.Working || member.Status == threadcontrol.Waiting) {
		live.LocalStop, live.Waiting = stop.Turn, stop.Wait
		if member.Wait != nil && member.Wait.State == "registered" {
			live.Waiting = member.Wait.ID
		}
	}
	lifecycle := attached.session.Lifecycle(live.Thread)
	lifecycle.Paused = member.Status == threadcontrol.Paused || member.Status == threadcontrol.Uncertain
	lifecycle.SetWaiting(live.LocalStop != "", now)
	if !lifecycle.Waiting && member.HostRunning {
		lifecycle.ObserveActive(true, now)
	}
	wait := threads.shared.WaitPublication(live.Thread)
	lifecycle.WaitingFor = ""
	if !lifecycle.Waiting || wait == nil {
		lifecycle.Wait = nil
	}
	if wait != nil {
		if wait.All {
			wait.Targets = []string{"all"}
		}
		names := make([]string, len(wait.Targets))
		for i, target := range wait.Targets {
			names[i] = pathdisplay.AgentDisplayName(target)
		}
		if lifecycle.Waiting {
			lifecycle.WaitingFor = strings.Join(names, ", ")
			value := activity.Value{Kind: "progress", Body: "Waiting for", Live: true}
			for _, target := range wait.Targets {
				value.WaitTargets = append(value.WaitTargets, activity.WaitTarget{Name: target})
			}
			if wait.EffectiveMS != nil {
				value.WaitTimeout = (time.Duration(*wait.EffectiveMS) * time.Millisecond).String()
			}
			if lifecycle.Wait == nil || lifecycle.Wait.WaitTimeout != value.WaitTimeout || !slices.Equal(lifecycle.Wait.WaitTargets, value.WaitTargets) {
				// Published values are immutable; keep unchanged roster identity stable.
				lifecycle.Wait = &value
			}
		}
		input := attached.threadInput
		// Wake acknowledgements are retained owner facts, not ordinary host
		// tool-completion items. Publish their actual reason before continuing.
		if (member.Wait.Stop == "committed" || input.waitID == wait.ID) && (wait.State == "completed" || wait.State == "timeout" || wait.State == "canceled" || wait.State == "paused") && (input.waitID != wait.ID || input.waitState != wait.State) {
			value := &activity.Value{Kind: "progress", Body: "Finished waiting for", WaitReason: wait.State}
			for _, target := range wait.Targets {
				value.WaitTargets = append(value.WaitTargets, activity.WaitTarget{Name: target})
			}
			if event := wait.Event; event != nil {
				value.WaitReason = string(event.Status)
				if wait.All || len(wait.Targets) > 1 {
					value.WaitReason = fmt.Sprintf("%s: %s", pathdisplay.AgentDisplayName(event.Task), event.Status)
				}
				if event.Kind == "message" {
					value.WaitReason = "message received from " + pathdisplay.AgentDisplayName(event.Task)
				}
				if event.Kind == "delivery_failed" {
					value.WaitReason = "delivery failed from " + pathdisplay.AgentDisplayName(event.Task)
				}
			}
			entry := sessionpresentation.ActivityEntry{Agent: "/root", Kind: "progress", Text: value.ProgressText(), Observed: now,
				Native: &sessionpresentation.NativeItem{Thread: live.Thread, Turn: member.Wait.Turn, Item: "threadcontrol:wait:" + wait.ID, Phase: "supplement", Wait: value}}
			entry.Seq = attached.session.Activity().NextSequence()
			sessionpresentation.ApplyActivity(attached.source, attached.childSource, []sessionpresentation.ActivityEntry{entry}, nil)
		}
		input.waitID, input.waitState = wait.ID, wait.State
	}
	if live.Turn == "" {
		switch member.Status {
		case threadcontrol.Completed:
			if stopped != "" {
				lifecycle.TurnCompleted(true, now)
				attached.presentation.Status.Complete("completed", nil, now)
			}
		case threadcontrol.Paused, threadcontrol.Uncertain:
			lifecycle.Final = false
			attached.presentation.Status.Set("Paused", false)
		case threadcontrol.Interrupted:
			if stopped != "" {
				attached.presentation.Status.Complete("interrupted", nil, now)
			}
		}
	}
	roster := sessionpresentation.ActivityEvent{Agents: attached.session.Roster()}
	attached.source.Apply(roster)
	attached.childSource.Apply(roster)
}

// One pending admission borrows the driver; payloads stay in Conversation until
// reconciliation completes. The host event loop keeps serving receipts/cancel.
type threadInput struct {
	driver            *threadbinding.Driver
	pending           chan humanInputAdmission
	cancel            context.CancelFunc
	ready             bool
	admitted          bool
	caller, role      string
	id                string
	waitID, waitState string
	completions       []logicalCompletion
	effect            *threadcontrol.Effect
	interruptions     *threadcontrol.Interruptions
	restoration       chan error
	restoreErr        error
}

type humanInputAdmission struct {
	effect *threadcontrol.Effect
	err    error
}

func (p *threadInput) preparing() bool {
	return p != nil && (p.restoration != nil || p.pending != nil)
}

func (p *threadInput) prepare(ctx context.Context, thread string, requests *sessioncontrol.Requests[appServerRequest]) (bool, error) {
	if p == nil {
		return true, nil
	}
	if p.restoration != nil {
		select {
		case p.restoreErr = <-p.restoration:
			p.restoration = nil
		default:
			return false, nil
		}
	}
	if p.restoreErr != nil || p.ready {
		return p.ready, p.restoreErr
	}
	if p.pending == nil {
		ctx, p.cancel = context.WithCancel(ctx)
		p.pending = make(chan humanInputAdmission)
		p.id = rand.Text()
		done, id, driver := p.pending, p.id, p.driver
		go func() {
			var effect *threadcontrol.Effect
			err := requests.ConfirmPreparedInput(ctx, thread)
			if err == nil {
				effect, err = driver.HumanInput(ctx, thread, id)
			}
			select {
			case done <- humanInputAdmission{effect, err}:
			case <-ctx.Done():
				if effect != nil {
					driver.HumanInputAcknowledged(thread, id, "", &binding.DispatchError{Outcome: binding.NotDispatched, Cause: ctx.Err()})
				}
			}
		}()
	}
	select {
	case result := <-p.pending:
		p.cancel()
		p.pending, p.cancel = nil, nil
		p.effect, p.ready = result.effect, result.err == nil
		return p.ready, result.err
	default:
		return false, nil
	}
}

func (p *threadInput) reset() {
	if p == nil {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	if effect := p.effect; effect != nil {
		p.driver.HumanInputAcknowledged(effect.Target, effect.ID, "", &binding.DispatchError{Outcome: binding.NotDispatched, Cause: errors.New("input canceled before dispatch")})
	}
	p.pending, p.cancel, p.ready, p.effect, p.id = nil, nil, false, nil, ""
}
