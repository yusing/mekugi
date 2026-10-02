package router

import (
	"context"
	"net/http"
	"time"
)

type journalLatencyKey struct{}

// Request-local diagnostic timings, never persisted state or mutation inputs.
type journalLatency struct {
	deliveryWait  time.Duration
	stateWait     time.Duration
	replayWait    time.Duration
	persistWrite  time.Duration
	responseWrite time.Duration
}

func journalLatencyFor(ctx context.Context) *journalLatency {
	latency, _ := ctx.Value(journalLatencyKey{}).(*journalLatency)
	return latency
}

func (latency *journalLatency) record(debug *debugOutput, thread, call string, started time.Time) {
	fields := map[string]any{
		"event": "journal_latency", "schema_version": 1,
		"delivery_wait_us":  latency.deliveryWait.Microseconds(),
		"state_wait_us":     latency.stateWait.Microseconds(),
		"replay_wait_us":    latency.replayWait.Microseconds(),
		"persist_write_us":  latency.persistWrite.Microseconds(),
		"response_write_us": latency.responseWrite.Microseconds(),
		"total_us":          time.Since(started).Microseconds(),
	}
	for key, value := range map[string]string{"thread_id": thread, "call_id": call} {
		if safeFeatureIdentity(value) {
			fields[key] = value
		}
	}
	debug.event(fields)
}

type journalLatencyWriter struct {
	http.ResponseWriter
	latency *journalLatency
}

func (w journalLatencyWriter) Write(data []byte) (int, error) {
	started := time.Now()
	n, err := w.ResponseWriter.Write(data)
	w.latency.responseWrite += time.Since(started)
	return n, err
}
