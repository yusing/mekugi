package router

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxLiveDiffEventBytes = 1 << 20

const liveDiffEventsPath = "/internal/live-diff"
const liveActivityEventsPath = "/internal/live-activity"

// Test-only bridges let existing subprocess rendering tests drive local state.
// The bootstrap file is a private connection capability, not changing view data.
type liveDiffConnection struct {
	Endpoint string
	Token    string
}

var testLiveConnections sync.Map

func testLiveConnection(owner any) liveDiffConnection {
	value, _ := testLiveConnections.LoadOrStore(owner, liveDiffConnection{Token: rand.Text()})
	return value.(liveDiffConnection)
}
func (b *liveDiffBroker) setEndpoint(endpoint string) {
	c := testLiveConnection(b)
	c.Endpoint = endpoint
	testLiveConnections.Store(b, c)
}
func (b *liveDiffBroker) descriptor() liveDiffConnection { return testLiveConnection(b) }
func (a *subagentActivity) setPaneEndpoint(endpoint string) {
	c := testLiveConnection(a)
	c.Endpoint = endpoint
	testLiveConnections.Store(a, c)
}
func (a *subagentActivity) paneDescriptor() liveDiffConnection { return testLiveConnection(a) }
func (b *liveDiffBroker) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+b.descriptor().Token {
		http.Error(w, "invalid live diff capability", http.StatusUnauthorized)
		return
	}
	sub := b.subscribe()
	defer b.unsubscribe(sub)
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	write := func(event liveDiffEvent) error {
		data, err := json.Marshal(event)
		if err != nil || len(data) > maxLiveDiffEventBytes {
			return errors.New("live diff event exceeds capacity")
		}
		if err := controller.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return err
		}
		if _, err := w.Write(append(data, '\n')); err != nil {
			return err
		}
		return controller.Flush()
	}
	// The snapshot barrier must precede the separate preview mailbox.
	if write(<-sub.events) != nil {
		return
	}
	// Restore retained turn state before replaceable preview snapshots.
	select {
	case event := <-sub.events:
		if write(event) != nil {
			return
		}
	default:
	}
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-b.ctx.Done():
			_ = write(liveDiffEvent{Kind: "end"})
			return
		case <-r.Context().Done():
			return
		case <-sub.gap:
			_ = write(liveDiffEvent{Kind: "reset"})
			return
		case <-sub.previewReady:
			for _, event := range b.takePreviews(sub) {
				if write(event) != nil {
					return
				}
			}
		case event := <-sub.events:
			if write(event) != nil {
				return
			}
		case <-heartbeat.C:
			if write(liveDiffEvent{Kind: "heartbeat"}) != nil {
				return
			}
		}
	}
}

func liveDiffRequest(ctx context.Context, connection liveDiffConnection, method string, body io.Reader) (*http.Request, error) {
	if !strings.HasPrefix(connection.Endpoint, "http://127.0.0.1:") {
		return nil, fmt.Errorf("invalid local live diff endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, method, connection.Endpoint, body)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+connection.Token)
	}
	return req, err
}
