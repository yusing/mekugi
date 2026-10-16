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
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > (1<<20) {
		return connection, errors.New("invalid live diff connection")
	}
	if json.Unmarshal(data, &connection) != nil || connection.Endpoint == "" || connection.Token == "" {
		return connection, errors.New("invalid live diff connection")
	}
	return connection, nil
}

// A bounded decoder queue propagates backpressure only to the event connection,
// never the edit producer. Reconnection begins with a fresh snapshot barrier.
func liveDiffStream(ctx context.Context, connection liveDiffConnection, output chan<- reviewEvent) {
	defer close(output)
	send := func(event reviewEvent) bool {
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
			send(reviewEvent{Kind: "error", Status: err.Error()})
			return
		}
		response, err := client.Do(req)
		if err == nil && response.StatusCode != http.StatusOK {
			response.Body.Close()
			send(reviewEvent{Kind: "error", Status: "router rejected live diff connection"})
			return
		}
		if err == nil {
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 4096), maxLiveDiffEventBytes)
			for scanner.Scan() {
				var event reviewEvent
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
			send(reviewEvent{Kind: "error", Status: "router live diff connection is unavailable"})
			return
		}
		if !send(reviewEvent{Kind: "coverage", Status: "RECONNECTING: live updates interrupted"}) {
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
