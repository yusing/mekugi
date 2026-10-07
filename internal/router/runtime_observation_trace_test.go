package router

import (
	"bytes"
	json "encoding/json/v2"
	"io"
	"net/http"
	"sync"
	"testing"
)

// This recorder is used only in isolated native fixtures. Production observations
// never print model commands, tool inputs, or private capabilities.
type nativeObservationTrace struct {
	mu            sync.Mutex
	before, after map[string]ObservationCall
}

func traceNativeObservation(t *testing.T, service *ObservationService) *nativeObservationTrace {
	t.Helper()
	trace := &nativeObservationTrace{before: make(map[string]ObservationCall), after: make(map[string]ObservationCall)}
	handler := service.server.Handler
	service.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err == nil {
			var request observationRequest
			if json.Unmarshal(data, &request) == nil {
				trace.mu.Lock()
				if request.Operation == "before" {
					trace.before[request.Call.ID] = request.Call
				}
				if request.Operation == "after" {
					trace.after[request.Call.ID] = request.Call
				}
				trace.mu.Unlock()
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		handler.ServeHTTP(w, r)
	})
	return trace
}

func (trace *nativeObservationTrace) logDrift(t *testing.T) {
	t.Helper()
	trace.mu.Lock()
	defer trace.mu.Unlock()
	for id, got := range trace.after {
		if want, ok := trace.before[id]; ok && !sameObservationCall(&want, &got) {
			t.Logf("isolated native fixture drift: before=%+v after=%+v", want, got)
		}
	}
}
