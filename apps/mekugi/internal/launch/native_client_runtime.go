package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

const (
	nativeSemanticCall = iota
	nativeInputCall
	nativeObservationCall
	nativeReceiptCall
	nativeCancellationCall
	nativeNavigationCall
	nativePreferenceCall
	nativeAcquisitionCall
	nativeCallLanes
)

// The physical terminal has one active interaction. Its source interaction stays
// alive while an admitted request or its retained consequences are unsettled.
// Request lanes are shared by every interaction, including hidden sources.
// In particular, changing views cannot start a second input preparation.
type nativeClientRuntime struct {
	previews       imagePreviews
	ctx            context.Context
	active         *nativeClient
	owners         []*nativeClient
	messages       chan nativeClientMessage
	results        chan *nativeClientCall
	reattached     chan nativeClientReattached
	requests       [nativeCallLanes]*nativeClientCall
	inputOwner     *nativeClient
	clipboardOwner *nativeClient
	pasteOwner     *nativeClient
	editors        map[[2]string]*composer.Editor
	images         map[string]bool
	unknown        []nativeUnknownInput
}

type nativeClientMessage struct {
	owner       *nativeClient
	event       *sessionapi.NativeEvent
	clipboard   *nativeClipboardResult
	restoration *nativeRestoredEffect
	err         error
}

func (r *nativeClientRuntime) decode(key byte, now time.Time) {
	owner := r.active
	if r.pasteOwner != nil {
		owner = r.pasteOwner
	}
	if event := owner.renderer.terminal.Decoder.Feed(key, now); event.Kind != terminalsession.NoInput {
		if event.Kind == terminalsession.PasteStart {
			r.pasteOwner = owner
		}
		owner.queueInput(event)
	}
}

// drainKeys settles queued continuations before expiring a lone Escape. Bound
// the drain so sustained input cannot starve rendering or host events; reaching
// the budget defers expiry until a later tick observes an empty input queue.
func (r *nativeClientRuntime) drainKeys(keys <-chan byte, now time.Time) {
	for range 256 {
		select {
		case key, ok := <-keys:
			if !ok {
				// Let the loop dispatch drained input before observing EOF.
				return
			}
			r.decode(key, time.Now())
		default:
			c := r.active
			if c.renderer.terminal.Decoder.Escape(now) {
				c.queueInput(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b"})
			}
			return
		}
	}
}

func nativeCallLane(operation sessionapi.NativeOperation) int {
	switch {
	case operation.Answers != nil || operation.Composer != nil && !strings.HasPrefix(strings.TrimSpace(operation.Composer.Draft.Text), "/"):
		return nativeInputCall
	case operation.Output != nil:
		return nativeObservationCall
	case operation.DetailOutput != nil || operation.Completion != nil:
		return nativeAcquisitionCall
	case operation.Receipt != nil:
		return nativeReceiptCall
	case operation.Interrupt != nil:
		return nativeCancellationCall
	case operation.Preferences != nil:
		return nativePreferenceCall
	case operation.Display != nil || operation.Focused != nil:
		return nativeNavigationCall
	default:
		return nativeSemanticCall
	}
}

func (c *nativeClient) request(lane int) *nativeClientCall {
	if c.runtime != nil {
		if call := c.runtime.requests[lane]; call != nil && call.owner == c {
			return call
		}
	}
	return nil
}

func (r *nativeClientRuntime) start(c *nativeClient, stdout *os.File) {
	c.ctx, c.stop = context.WithCancel(r.ctx)
	c.runtime = r
	c.notifications.out, c.notifications.focused = stdout, true
	c.ownedImages = make(map[string]bool)
	c.readStream(c.connection)
	r.owners = append(r.owners, c)
}

func (r *nativeClientRuntime) deliver(message nativeClientMessage) error {
	c := message.owner
	switch {
	case message.event != nil:
		previous := c.event.Frame.View.Status.Feedback
		c.accept(*message.event)
		if feedback := c.event.Frame.View.Status.Feedback; c != r.active && feedback.Alert && feedback.Text != "" && feedback != previous {
			r.active.feedback.Set(fmt.Sprintf("Session %s: %s", c.event.Frame.View.Main.ID, feedback.Text), true, time.Now())
			r.active.refresh(time.Now())
		}
	case message.clipboard != nil:
		c.clipboardResult(*message.clipboard)
	case message.restoration != nil:
		c.restorationResult(*message.restoration)
	case message.err != nil:
		err := message.err
		if errors.Is(err, io.EOF) {
			err = errors.New("native server connection closed")
		}
		if c == r.active {
			if outcome, ok := errors.AsType[*sessionapi.NativeExit](err); ok && outcome.Code == 0 {
				c.quit = true
				return nil
			}
			return err
		}
		r.closeOwner(c)
		if outcome, ok := errors.AsType[*sessionapi.NativeExit](err); !ok || outcome.Code != 0 {
			r.active.feedback.Set(fmt.Sprintf("Session %s: %v", c.event.Frame.View.Main.ID, err), true, time.Now())
			r.active.refresh(time.Now())
		}
	}
	return nil
}

// Input capacity is reserved before taking a draft. A second submission remains
// editable in its existing editor while the first admission/outcome is pending.
func (c *nativeClient) inputAvailable() bool {
	if c.runtime == nil {
		return true
	}
	r := c.runtime
	if r.inputOwner != nil && r.inputOwner != c {
		return false
	}
	if slices.ContainsFunc(r.requests[:], func(call *nativeClientCall) bool {
		return call != nil && (call.operation.Composer != nil || call.operation.Answers != nil)
	}) {
		return false
	}
	for _, owner := range r.owners {
		if slices.ContainsFunc(owner.calls, func(call *nativeClientCall) bool {
			return call.operation.Composer != nil || call.operation.Answers != nil
		}) {
			return false
		}
	}
	return true
}

func (r *nativeClientRuntime) schedule() {
	c := r.active
	if c.reattach != "" && !c.attaching {
		var retained *nativeClient
		for _, owner := range r.owners {
			if owner != c && !owner.closed && owner.event.Frame.View.Main.ID == c.reattach {
				retained = owner
				break
			}
		}
		if retained != nil {
			c.reattach = ""
			r.activate(retained)
			c = retained
		} else {
			c.attachDestination(c.ctx, r.reattached)
		}
	}
	if c.request(nativeAcquisitionCall) == nil && !slices.ContainsFunc(c.calls, func(call *nativeClientCall) bool { return nativeCallLane(call.operation) == nativeAcquisitionCall }) {
		c.acquireCompletion()
	}
	for _, owner := range r.owners {
		if owner.closed {
			continue
		}
		for i := 0; i < len(owner.calls); {
			call := owner.calls[i]
			lane := nativeCallLane(call.operation)
			input := call.operation.Composer != nil || call.operation.Answers != nil
			if r.requests[lane] != nil || input && (r.inputOwner != nil && r.inputOwner != owner || slices.ContainsFunc(r.requests[:], func(pending *nativeClientCall) bool {
				return pending != nil && (pending.operation.Composer != nil || pending.operation.Answers != nil)
			})) {
				i++
				continue
			}
			owner.calls = slices.Delete(owner.calls, i, i+1)
			call.owner = owner
			r.requests[lane] = call
			if input {
				r.inputOwner = owner
			}
			r.send(call, call.operation)
		}
	}
}

func (r *nativeClientRuntime) send(call *nativeClientCall, operation sessionapi.NativeOperation) {
	owner := call.owner
	ctx, cancel := context.WithCancel(owner.ctx)
	call.cancel, call.complete = cancel, false
	connection := owner.connection
	owner.workers.Go(func() {
		defer cancel()
		call.result, call.err = connection.Send(ctx, call.target, operation)
		select {
		case r.results <- call:
		case <-owner.ctx.Done():
		}
	})
}

func (r *nativeClientRuntime) settle() {
	for lane, call := range r.requests {
		if call == nil || !call.complete {
			continue
		}
		c := call.owner
		version := c.event.Frame.View.Version
		settled := call.discard || call.err != nil || version.Attachment == call.result.Version.Attachment && version.Revision >= call.result.Version.Revision
		if call.operation.Output != nil && call.err == nil && !call.hydrated && !call.discard {
			// Only a delivered catch-up marker permits read reacquisition. Read
			// one unresolved identity at a time so a large batch cannot repeatedly
			// evict its own earlier results. Keep the original call and callback.
			if settled && call.reacquire && (call.outputLost || call.reading != nil && !slices.Contains(call.unresolved, *call.reading)) {
				call.reading = new(call.unresolved[0])
				call.outputLost = false
				operation := call.operation
				output := *operation.Output
				output.Demands = []activity.OutputDemand{*call.reading}
				operation.Output = &output
				r.send(call, operation)
			}
			settled = false
		}
		if !settled {
			continue
		}
		r.requests[lane] = nil
		if call.discard {
			call.err = nil
		}
		if call.after != nil {
			call.after(call.err)
		}
		if call.err != nil {
			c.feedback.Set(call.err.Error(), true, time.Now())
			if c != r.active {
				r.active.feedback.Set(fmt.Sprintf("Session %s: %v", c.event.Frame.View.Main.ID, call.err), true, time.Now())
				r.active.refresh(time.Now())
			}
		} else {
			if receipt := call.operation.Receipt; receipt != nil {
				c.ackEffect = max(c.ackEffect, receipt.Effect)
			}
			if call.next != nil {
				// The continuation consumes the originating editor, even when an
				// independent display request has selected another input meanwhile.
				c.withInput(call.nextInput, call.nextView, call.nextEditor, func() { c.continueKey(*call.next) })
			}
		}
		c.refresh(time.Now())
	}
	if c := r.inputOwner; c != nil && c.request(nativeInputCall) == nil && (c.request(nativeSemanticCall) == nil || c.request(nativeSemanticCall).operation.Composer == nil) && c.effectsSettled() {
		r.inputOwner = nil
	}
}

func (c *nativeClient) effectsSettled() bool {
	return !c.event.Frame.PendingEffects && len(c.prompts.submitted) == 0 && c.restoration == nil && !slices.ContainsFunc(c.event.Frame.Effects, func(effect sessionapi.NativeEffect) bool { return effect.ID > c.ackEffect })
}

func (r *nativeClientRuntime) activate(c *nativeClient) {
	previous := r.active
	previous.notifications.focused = false
	previous.stagePreferences(true)
	// Hydration belongs to a painted source, not to the terminal's control path.
	// An unpainted source can reacquire it when selected again.
	dependent := previous.selection != nil && len(previous.selection.input) != 0
	if !dependent {
		previous.selection = nil
	}
	if call := previous.request(nativeObservationCall); call != nil && call.operation.Output != nil && !dependent {
		call.discard = true
		call.cancel()
	}
	previous.calls = slices.DeleteFunc(previous.calls, func(call *nativeClientCall) bool {
		if call.operation.Output == nil || dependent {
			return false
		}
		if call.after != nil {
			call.after(nil)
		}
		return true
	})
	r.active = c
	if operation := previous.attachOperation; operation != nil {
		previous.attachOperation = nil
		if operation.Details != nil {
			c.details(c.event.Frame.View.Version, *operation.Details)
		} else if operation.Display != nil {
			operation.Display.Version = c.event.Frame.View.Version
			c.enqueue(*operation, nil, nil)
		}
	}
	r.previews.reset()
	c.renderer.terminal.Frame.Images = previous.renderer.terminal.Frame.Images
	if previous.event.Frame.Family != nil && c.event.Frame.Family != nil && previous.event.Frame.Family.Root == c.event.Frame.Family.Root {
		from, to := previous.renderer.terminal, c.renderer.terminal
		layout := to.Shell.Preferences(to.Main)
		current := from.Shell.Preferences(from.Main)
		layout.Focus, layout.Split, layout.DiffOpen, layout.NavigatorColumns = current.Focus, current.Split, current.DiffOpen, current.NavigatorColumns
		to.Shell.RestorePreferences(layout, to.Main)
	}
	c.notifications.focused = true
	c.enqueue(sessionapi.NativeOperation{Focused: new(true)}, nil, nil)
	c.renderer.terminal.Frame.Rows = nil
	c.refresh(time.Now())
}

func (r *nativeClientRuntime) reattach(result nativeClientReattached, stdout *os.File) {
	c := result.owner
	if c.closed || c != r.active || c.reattach != "" {
		c.attaching = false
		if result.connection != nil {
			result.connection.Close()
		}
		if c.reattach == c.event.Frame.View.Main.ID {
			c.reattach = ""
		}
		return
	}
	next := c.applyReattachment(result)
	if next == nil {
		return
	}
	r.start(next, stdout)
	next.accept(result.event)
	r.activate(next)
}

func (r *nativeClientRuntime) retire() {
	for _, c := range r.owners {
		if c == r.active || c == r.pasteOwner || c.closed || c.attaching || c.handoff != nil || c.clipboardPending || len(c.calls) != 0 || !c.effectsSettled() {
			continue
		}
		if len(c.event.Frame.Questions) > 0 || len(c.event.Frame.Approvals) > 0 {
			continue
		}
		if slices.ContainsFunc(r.requests[:], func(call *nativeClientCall) bool { return call != nil && call.owner == c }) {
			continue
		}
		r.closeOwner(c)
	}
	r.owners = slices.DeleteFunc(r.owners, func(c *nativeClient) bool { return c.closed && c != r.pasteOwner })
}

func (r *nativeClientRuntime) closeOwner(c *nativeClient) {
	if c.closed {
		return
	}
	c.closed = true
	c.stop()
	c.connection.Close()
	c.workers.Wait()
	c.finishPendingCalls()
	if r.inputOwner == c {
		r.inputOwner = nil
	}
	if r.clipboardOwner == c {
		r.clipboardOwner = nil
	}
	c.renderer.terminal.Diff.Close()
	c.renderer.terminal.Prompts.Secret = composer.Editor{}
	c.secrets, c.prompts = nil, nativeClientPrompts{}
	for path := range c.ownedImages {
		r.images[path] = true
	}
	r.unknown = append(r.unknown, c.unknown...)
	c.unknown = nil
}

func (r *nativeClientRuntime) close() {
	for _, c := range r.owners {
		r.closeOwner(c)
	}
	select {
	case result := <-r.reattached:
		if result.connection != nil {
			result.connection.Close()
		}
	default:
	}
	r.active.unknown = append(r.active.unknown, r.unknown...)
	for path := range r.images {
		_ = os.Remove(path)
	}
}
