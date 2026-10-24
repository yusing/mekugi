package launch

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

func (c *nativeClient) readStream(connection *sessionapi.NativeConnection) {
	// Hand off each frame before its following terminal outcome.
	c.workers.Go(func() {
		for {
			event, err := connection.Next()
			message := nativeClientMessage{owner: c, err: err}
			if err == nil {
				message.event = &event
			}
			select {
			case c.runtime.messages <- message:
			case <-c.ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	})
}

type nativeClientReattached struct {
	owner              *nativeClient
	displayParent      *nativeClient
	source, connection *sessionapi.NativeConnection
	event              sessionapi.NativeEvent
	err                error
	preferenceError    error
}

func (c *nativeClient) attachDestination(ctx context.Context, done chan<- nativeClientReattached) {
	source, thread := c.connection, c.reattach
	displayParent := c.attachParent
	target := c.event.Target()
	var preferences *sessionapi.NativePreferenceUpdate
	var preferenceError error
	if c.preferenceReady && target.Thread != "" {
		u := c.renderer.terminal
		data, err := c.preferences.Encode(u.Shell.Preferences(u.Main))
		preferenceError = err
		if err == nil {
			preferences = &sessionapi.NativePreferenceUpdate{Presentation: data, Flush: true}
		}
	}
	c.reattach, c.attaching, c.attachParent = "", true, nil
	c.workers.Go(func() {
		result := nativeClientReattached{owner: c, source: source, displayParent: displayParent, preferenceError: preferenceError}
		destination := source
		if displayParent != nil {
			destination = displayParent.connection
		}
		result.connection, result.err = destination.Attach(ctx, thread)
		if result.err == nil {
			result.event, result.err = result.connection.Next()
		}
		if result.err != nil {
			if result.connection != nil {
				result.connection.Close()
			}
			result.err = fmt.Errorf("Could not attach to session %s: %w", thread, result.err)
		} else if preferences != nil {
			// Keep the source's writer and view alive until the destination has
			// published, then flush the captured layout through its actual owner.
			flush, cancel := context.WithTimeout(ctx, time.Second)
			_, result.preferenceError = source.Send(flush, target, sessionapi.NativeOperation{Preferences: preferences})
			cancel()
		}
		select {
		case done <- result:
		case <-ctx.Done():
			if result.connection != nil {
				result.connection.Close()
			}
		}
	})
}

func (c *nativeClient) applyReattachment(result nativeClientReattached) *nativeClient {
	c.attaching = false
	if result.err != nil {
		c.feedback.Set(result.err.Error(), true, time.Now())
		c.refresh(time.Now())
		return nil
	}
	if result.source != c.connection {
		result.connection.Close()
		return nil
	}
	terminal, _ := c.renderer.views.NewAttachment(c.renderer.terminal.Transcript.Painter.Theme)
	terminal.Faint = c.renderer.terminal.Faint
	terminal.Frame.Clipboard = c.renderer.terminal.Frame.Clipboard
	terminal.Width, terminal.Height = c.renderer.terminal.Width, c.renderer.terminal.Height
	next := &nativeClient{connection: result.connection, displayParent: result.displayParent, renderer: nativeClientRenderer{terminal: terminal, views: c.renderer.views}}
	if result.preferenceError != nil {
		next.feedback.Set("Could not save outgoing session layout: "+result.preferenceError.Error(), true, time.Now())
	}
	return next
}

// Display navigation borrows the selected live runtime instead of forwarding
// its input through the parent's observational presentation.
func (c *nativeClient) displayThread(thread sessionpresentation.Thread) {
	parent := c
	if c.displayParent != nil && !c.displayParent.closed {
		parent = c.displayParent
	}
	if c.runtime != nil && parent.event.Frame.View.ReadOnly != sessionpresentation.Replay &&
		(thread.ID == parent.event.Frame.View.Main.ID || slices.Contains(parent.event.Frame.ChildThreads, thread.ID)) {
		if thread.ID == parent.event.Frame.View.Main.ID && parent.event.Frame.View.Displayed.ID != thread.ID {
			parent.enqueue(sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: parent.event.Frame.View.Version, Thread: thread}}, nil, nil)
		}
		if thread.ID != c.event.Frame.View.Main.ID || c.attaching {
			c.reattach, c.attachParent = thread.ID, parent
		}
		return
	}
	// Historical children outside the admitted runtime tree remain observational.
	if parent != c {
		c.runtime.activate(parent)
	}
	parent.enqueue(sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: parent.event.Frame.View.Version, Thread: thread}}, nil, nil)
}

// finishPendingCalls runs after request workers join. A dispatched request keeps
// its actual outcome; queued requests are definitively unadmitted and restore in
// original order. Detachment never retries a mutation.
func (c *nativeClient) finishPendingCalls() {
	if c.runtime != nil {
		for lane := range nativeCallLanes {
			if call := c.request(lane); call != nil {
				c.runtime.requests[lane] = nil
				if call.after != nil {
					call.after(call.err)
				}
			}
		}
	}
	refusal := &sessionapi.NativeFailure{Status: http.StatusConflict, Reason: "The native view closed before admission"}
	for i := len(c.calls) - 1; i >= 0; i-- {
		if c.calls[i].after != nil {
			c.calls[i].after(refusal)
		}
	}
	c.calls = nil
}
