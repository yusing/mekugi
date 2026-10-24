package launch

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

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
	stopped := live.LocalStop != ""
	live.LocalStop, live.Waiting = "", ""
	if stop := member.Stop; stop != nil && stop.Turn == member.Turn && stop.Generation == member.Generation && (member.Status == threadcontrol.Working || member.Status == threadcontrol.Waiting) {
		live.LocalStop = stop.Turn
		if member.Wait != nil {
			live.Waiting = member.Wait.ID
		}
	}
	lifecycle := attached.session.Lifecycle(live.Thread)
	lifecycle.Paused = member.Status == threadcontrol.Paused || member.Status == threadcontrol.Uncertain
	lifecycle.SetWaiting(live.LocalStop != "", now)
	if live.Turn == "" {
		switch member.Status {
		case threadcontrol.Paused, threadcontrol.Uncertain:
			lifecycle.Final = false
			attached.presentation.Status.Set("Paused", false)
		case threadcontrol.Interrupted:
			if stopped {
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
	driver       *threadbinding.Driver
	pending      chan humanInputAdmission
	cancel       context.CancelFunc
	ready        bool
	admitted     bool
	caller, role string
	id           string
	effect       *threadcontrol.Effect
	restoration  chan error
	restoreErr   error
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
