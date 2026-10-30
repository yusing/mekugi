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
	source, connection *sessionapi.NativeConnection
	event              sessionapi.NativeEvent
	err                error
}

func (c *nativeClient) attachDestination(ctx context.Context, done chan<- nativeClientReattached) {
	source, thread := c.connection, c.reattach
	c.reattach, c.attaching = "", true
	c.workers.Go(func() {
		result := nativeClientReattached{owner: c, source: source}
		result.connection, result.err = source.Attach(ctx, thread)
		if result.err == nil {
			result.event, result.err = result.connection.Next()
		}
		if result.err != nil {
			if result.connection != nil {
				result.connection.Close()
			}
			result.err = fmt.Errorf("Could not attach to session %s: %w", thread, result.err)
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
		c.attachOperation = nil
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
	next := &nativeClient{connection: result.connection, renderer: nativeClientRenderer{terminal: terminal, views: c.renderer.views}}
	return next
}

// Display navigation borrows the selected live runtime instead of forwarding
// its input through the parent's observational presentation.
func (c *nativeClient) displayThread(thread sessionpresentation.Thread) {
	c.attachOperation = nil
	family := c.event.Frame.Family
	if c.runtime != nil && family != nil && c.event.Frame.View.ReadOnly != sessionpresentation.Replay {
		if thread.ID == family.Root || slices.Contains(family.Threads, thread.ID) {
			if thread.ID != c.event.Frame.View.Main.ID || c.attaching {
				c.reattach = thread.ID
			} else if c.event.Frame.View.Displayed.ID != thread.ID {
				c.enqueue(sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: c.event.Frame.View.Version, Thread: thread}}, nil, nil)
			}
			return
		}
		// Historical content belongs to the family's retained source, never to
		// an independently started runtime.
		if family.Root != c.event.Frame.View.Main.ID {
			c.reattach = family.Root
			c.attachOperation = &sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Thread: thread}}
			return
		}
	}
	c.enqueue(sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: c.event.Frame.View.Version, Thread: thread}}, nil, nil)
}

// finishPendingCalls runs after request workers join. A dispatched request keeps
// its actual outcome; queued requests are definitively unadmitted and restore in
// original order. Detachment never retries a mutation.
func (c *nativeClient) finishPendingCalls() {
	if c.runtime != nil {
		for lane := range nativeCallLanes {
			if call := c.request(lane); call != nil {
				c.requests[lane] = nil
				if call.after != nil {
					call.after(call.err)
				}
			}
		}
	}
	refusal := &sessionapi.NativeFailure{Status: http.StatusConflict, Reason: "The native view closed before admission"}
	for _, v := range slices.Backward(c.calls) {
		if v.after != nil {
			v.after(refusal)
		}
	}
	c.calls = nil
}
