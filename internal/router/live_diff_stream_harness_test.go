package router

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"
)

func readLiveDiffConnection(path string) (liveDiffConnection, error) {
	file, err := os.Open(path)
	if err != nil {
		return liveDiffConnection{}, err
	}
	defer file.Close()
	var connection liveDiffConnection
	data, err := io.ReadAll(io.LimitReader(file, maxLiveDiffScopeBytes+1))
	if err != nil || len(data) > maxLiveDiffScopeBytes {
		return connection, errors.New("invalid live diff connection")
	}
	if json.Unmarshal(data, &connection) != nil || connection.Endpoint == "" || connection.Token == "" {
		return connection, errors.New("invalid live diff connection")
	}
	return connection, nil
}

// A bounded decoder queue propagates backpressure only to the event connection,
// never the edit producer. Reconnection begins with a fresh snapshot barrier.
func liveDiffStream(ctx context.Context, connection liveDiffConnection, output chan<- liveDiffEvent) {
	defer close(output)
	send := func(event liveDiffEvent) bool {
		select {
		case output <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 5 * time.Second}}
	defer client.CloseIdleConnections()
	failures := 0
	for ctx.Err() == nil {
		req, err := liveDiffRequest(ctx, connection, http.MethodGet, nil)
		if err != nil {
			send(liveDiffEvent{Kind: "error", Status: err.Error()})
			return
		}
		response, err := client.Do(req)
		if err == nil && response.StatusCode != http.StatusOK {
			response.Body.Close()
			send(liveDiffEvent{Kind: "error", Status: "router rejected live diff connection"})
			return
		}
		if err == nil {
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 4096), maxLiveDiffEventBytes)
			for scanner.Scan() {
				var event liveDiffEvent
				if json.Unmarshal(scanner.Bytes(), &event) != nil {
					break
				}
				if event.Kind == "reset" {
					break
				}
				if event.Kind == "scope" {
					failures = 0
				}
				if !send(event) || event.Kind == "end" {
					response.Body.Close()
					return
				}
			}
			response.Body.Close()
		}
		if ctx.Err() != nil {
			return
		}
		failures++
		if failures > 5 {
			send(liveDiffEvent{Kind: "error", Status: "router live diff connection is unavailable"})
			return
		}
		if !send(liveDiffEvent{Kind: "coverage", Status: "RECONNECTING: live updates interrupted"}) {
			return
		}
		timer := time.NewTimer(time.Duration(failures) * 200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
