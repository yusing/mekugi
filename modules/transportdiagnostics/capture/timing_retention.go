package capture

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const (
	maxTimingRequests     = 64
	maxTimingRecordBytes  = 512 << 10
	maxPendingTimings     = 128
	timingShutdownTimeout = 5 * time.Second
)

// TimingRecord uses the existing shared budget and session ownership/expiry.
// The extra kind budget prevents optional diagnostics from filling that store.
var TimingRecord = recordkind.Spec{ID: "request-timing", Match: recordkind.HashedName("request-timing-", ".json"), MaxRecordBytes: maxTimingRecordBytes, Limit: 64 << 20}

type timingWindow struct {
	Version  int             `json:"version"`
	Thread   string          `json:"thread_id"`
	Evicted  uint64          `json:"evicted_requests"`
	Requests []RequestTiming `json:"requests"`
}

type timingRetention struct {
	store   *history.Store
	limits  history.Limits
	notice  func(string, error)
	mu      sync.Mutex
	closed  bool
	pending chan pendingTiming
	done    chan struct{}
	cancel  context.CancelFunc
}

type pendingTiming struct {
	thread  string
	request RequestTiming
}

// RetainTimings binds the selected store before serving requests. It opens no
// network resource. Its bounded worker drains before Recorder.Close returns.
func (r *Recorder) RetainTimings(store *history.Store, limits history.Limits, notice func(string, error)) {
	if r != nil && store != nil {
		r.timings.close()
		ctx, cancel := context.WithCancel(context.Background())
		r.timings = &timingRetention{store: store, limits: limits, notice: notice, pending: make(chan pendingTiming, maxPendingTimings), done: make(chan struct{}), cancel: cancel}
		go r.timings.run(ctx, r)
	}
}

func (retention *timingRetention) close() {
	if retention == nil {
		return
	}
	retention.mu.Lock()
	if !retention.closed {
		retention.closed = true
		close(retention.pending)
		// Bound draining at recorder teardown, not during ordinary contention.
		timer := time.AfterFunc(timingShutdownTimeout, retention.cancel)
		defer timer.Stop()
	}
	retention.mu.Unlock()
	<-retention.done
}

func (retention *timingRetention) run(ctx context.Context, r *Recorder) {
	defer close(retention.done)
	defer retention.cancel()
	for pending := range retention.pending {
		if err := retention.publish(ctx, r, pending); err != nil && retention.notice != nil {
			retention.notice(pending.thread, err)
		}
	}
}

func timingName(thread string) string {
	return fmt.Sprintf("request-timing-%x.json", sha256.Sum256([]byte(thread)))
}

func readTimingWindow(store inspection.RetainedReader, thread string) (timingWindow, bool, error) {
	window := timingWindow{Version: 1, Thread: thread}
	data, err := store.ReadRetained(timingName(thread), maxTimingRecordBytes)
	if errors.Is(err, os.ErrNotExist) {
		return window, false, nil
	}
	if err != nil {
		return window, false, err
	}
	if err := json.Unmarshal(data, &window); err != nil {
		return window, false, err
	}
	if window.Version != 1 || window.Thread != thread || thread == "" || safeDiagnosticIdentifier(thread) != thread || len(window.Requests) == 0 || len(window.Requests) > maxTimingRequests {
		return window, false, errors.New("invalid retained request timing window")
	}
	identities := make(map[string]bool, len(window.Requests))
	for _, request := range window.Requests {
		if request.CaptureID == "" || safeDiagnosticIdentifier(request.CaptureID) != request.CaptureID || identities[request.CaptureID] || request.Sequence == 0 || request.Started.IsZero() || request.Completed == nil || !validTimingOutcome(request.Outcome) || len(request.Attempts) > maxTimingAttempts {
			return window, false, errors.New("invalid retained request timing identity")
		}
		identities[request.CaptureID] = true
		phases := []*TimingPhase{request.ToolResultReady, request.ForwardStarted, request.Completed}
		var previous uint64
		for _, attempt := range request.Attempts {
			if attempt.Number <= previous || (attempt.Transport != "http" && attempt.Transport != "websocket") || (attempt.Status != 0 && (attempt.Status < 100 || attempt.Status > 599)) ||
				(attempt.Outcome != "unknown" && attempt.Outcome != "completed" && attempt.Outcome != "failed") || (attempt.Sent != nil) != (attempt.SendObservations == 1) {
				return window, false, errors.New("invalid retained request timing attempt")
			}
			previous = attempt.Number
			phases = append(phases, &attempt.Started, attempt.Sent, attempt.FirstOutput, attempt.Completed)
		}
		for _, phase := range phases {
			if phase != nil && (phase.At.IsZero() || phase.OffsetMillis < 0 || math.IsNaN(phase.OffsetMillis) || math.IsInf(phase.OffsetMillis, 0)) {
				return window, false, errors.New("invalid retained request timing phase")
			}
		}
	}
	return window, true, nil
}

func (r *Recorder) retainTiming(state *requestState, at time.Time) {
	if r.timings == nil || state.threadID == "" {
		return
	}
	request := state.completedTiming(at)
	r.timings.mu.Lock()
	var err error
	if r.timings.closed {
		err = errors.New("request timing recorder is closed")
	} else {
		select {
		case r.timings.pending <- pendingTiming{thread: state.threadID, request: request}:
		default:
			err = errors.New("request timing publication queue is full; observation unavailable")
		}
	}
	r.timings.mu.Unlock()
	if err != nil && r.timings.notice != nil {
		r.timings.notice(state.threadID, err)
	}
}

func (retention *timingRetention) publish(ctx context.Context, r *Recorder, pending pendingTiming) error {
	// Only this bounded background worker waits behind correctness state.
	// Inference cancellation does not discard already observed timing facts.
	return retention.store.Transaction(ctx, r.writes, func(tx *history.Transaction) error {
		window, _, err := readTimingWindow(retention.store, pending.thread)
		if err != nil {
			return err
		}
		if len(window.Requests) == maxTimingRequests {
			copy(window.Requests, window.Requests[1:])
			window.Requests = window.Requests[:maxTimingRequests-1]
			window.Evicted++
		}
		window.Requests = append(window.Requests, pending.request)
		data, err := json.Marshal(&window)
		if err != nil {
			return err
		}
		name := timingName(pending.thread)
		if err := tx.Retain(pending.thread, retention.limits, name); err != nil {
			return err
		}
		if err := tx.Admit(retention.limits, name, int64(len(data))); err != nil {
			return err
		}
		return tx.Write(name, "request-timing-pending-", data)
	})
}

// InspectTiming reads only this thread's bounded record; it never opens a writer.
func (*Service) InspectTiming(ctx context.Context, store inspection.RetainedReader, thread string) (jsontext.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := struct {
		State           string `json:"state"`
		Coverage        string `json:"coverage"`
		FirstOutput     string `json:"first_output_definition"`
		ToolResultReady string `json:"tool_result_ready_state"`
		timingWindow
	}{State: "unavailable", Coverage: "recent completed observations only; crashes, unobserved requests and failed publications may be absent", FirstOutput: "first provider response body bytes or WebSocket message observed, not first generated token", ToolResultReady: "unavailable: no causal tool-result-to-request pairing"}
	var found bool
	var err error
	result.timingWindow, found, err = readTimingWindow(store, thread)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// This automatically selected evidence is auxiliary to the AX report.
		// Discard partially decoded facts and raw errors, not unrelated AX data.
		return jsontext.Value(`{"state":"error","reason":"retained request timing could not be read or validated"}`), nil
	}
	if found {
		result.State = "observed_window"
	}
	return json.Marshal(&result)
}
