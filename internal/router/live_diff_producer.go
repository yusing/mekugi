package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// A worker uses one connection for the lifetime of its private control channel.
// The router can detect a crash or dropped last notification from its closure.
type liveDiffProducer struct {
	mu     sync.Mutex
	queue  chan liveDiffProducerMessage
	cancel context.CancelFunc
	done   chan struct{}
	closed bool
}

func startLiveDiffProducer(ctx context.Context, connection liveDiffConnection) *liveDiffProducer {
	if connection.Endpoint == "" {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	req, err := liveDiffRequest(ctx, connection, http.MethodPost, reader)
	if err != nil {
		cancel()
		reader.Close()
		writer.Close()
		return nil
	}
	p := &liveDiffProducer{
		queue: make(chan liveDiffProducerMessage, 32), cancel: cancel, done: make(chan struct{}),
	}
	p.queue <- liveDiffProducerMessage{} // Establish coverage before any publication.
	go func() {
		defer writer.Close()
		encoder := json.NewEncoder(writer)
		for {
			select {
			case <-ctx.Done():
				return
			case message, open := <-p.queue:
				if !open {
					_ = encoder.Encode(liveDiffProducerMessage{Done: true})
					return
				}
				if encoder.Encode(message) != nil {
					cancel()
					return
				}
			}
		}
	}()
	go func() {
		defer close(p.done)
		defer cancel()
		stop := context.AfterFunc(ctx, func() { reader.Close(); writer.Close() })
		defer stop()
		client := &http.Client{Transport: &http.Transport{Proxy: nil}}
		defer client.CloseIdleConnections()
		response, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
		}
	}()
	return p
}

func (p *liveDiffProducer) publish(changes []liveDiffChange) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	select {
	case p.queue <- liveDiffProducerMessage{Changes: changes}:
	default:
		// Closing the connection invalidates coverage at the router. Never
		// block an edit or silently drop the final update behind a full queue.
		p.closed = true
		p.cancel()
	}
}

func (p *liveDiffProducer) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.queue)
	}
	p.mu.Unlock()
	defer p.cancel()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
	}
}
