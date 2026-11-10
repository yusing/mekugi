package capture

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func timingStore(t *testing.T) (*history.Service, *history.Store) {
	t.Helper()
	owner, err := history.NewService([]recordkind.Spec{TimingRecord})
	if err != nil {
		t.Fatal(err)
	}
	store, err := owner.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return owner, store
}

func TestTimingRetentionBoundedWindowAndAttemptWork(t *testing.T) {
	_, store := timingStore(t)
	r, err := new(Service).Open(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.RetainTimings(store, history.Limits{}, func(_ string, err error) { t.Error(err) })
	var last string
	for range maxTimingRequests + 10 {
		state := r.beginRequest(http.Header{"Thread-Id": {"thread"}})
		ctx := context.WithValue(t.Context(), captureKey{}, state)
		ObserveForwardStart(ctx)
		ObserveRequestOutcome(ctx, "completed")
		for range maxTimingAttempts + 5 {
			number := state.beginProviderAttempt()
			state.startTimingAttempt(number, "http", time.Now())
			state.sent(number)
			state.firstOutput(number)
			state.completeTimingAttempt(number, time.Now(), 200, false)
		}
		if len(state.timing.Attempts) != maxTimingAttempts || state.timing.OmittedAttempts != 5 {
			t.Fatalf("unbounded attempts: %+v", state.timing)
		}
		last = state.captureID
		r.retainTiming(state, time.Now())
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// Restart reads only the fixed window, not the invocation's metrics/cache.
	window, found, err := readTimingWindow(store, "thread")
	if err != nil || !found || len(window.Requests) != maxTimingRequests || window.Evicted != 10 || window.Requests[len(window.Requests)-1].CaptureID != last {
		t.Fatalf("window=%+v found=%v error=%v", window, found, err)
	}
	data, err := store.ReadRetained(timingName("thread"), maxTimingRecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("maximum-detail window: %d requests, %d attempts/request, %d bytes", len(window.Requests), maxTimingAttempts, len(data))
	if len(data) >= maxTimingRecordBytes {
		t.Fatal("window exceeds fixed admission bound")
	}
	info, err := os.Stat(filepath.Join(store.Directory(), timingName("thread")))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private timing mode: %v %v", info, err)
	}
	for _, request := range window.Requests {
		if request.ToolResultReady != nil {
			t.Fatal("invented tool-ready pairing")
		}
	}
}

func TestTimingRetentionConcurrentWriters(t *testing.T) {
	_, store := timingStore(t)
	const requests = 16
	r, err := new(Service).Open(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.RetainTimings(store, history.Limits{}, func(_ string, err error) { t.Error(err) })
	var wait sync.WaitGroup
	for range requests {
		wait.Go(func() {
			state := r.beginRequest(http.Header{"Thread-Id": {"shared-thread"}})
			r.retainTiming(state, time.Now())
		})
	}
	wait.Wait()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	window, found, err := readTimingWindow(store, "shared-thread")
	if err != nil || !found || len(window.Requests) != requests {
		t.Fatalf("concurrent requests=%d found=%v error=%v", len(window.Requests), found, err)
	}
	seen := map[string]bool{}
	for _, request := range window.Requests {
		if seen[request.CaptureID] {
			t.Fatal("duplicate request identity across invocation restarts")
		}
		seen[request.CaptureID] = true
	}
}

func TestTimingUnknownHistoricalAndUnpairableEvidence(t *testing.T) {
	owner, store := timingStore(t)
	missing := owner.Attach(filepath.Join(t.TempDir(), "absent"))
	data, err := new(Service).InspectTiming(t.Context(), missing, "historical")
	if err != nil || !strings.Contains(string(data), `"state":"unavailable"`) {
		t.Fatalf("historical evidence=%s error=%v", data, err)
	}
	if _, err := os.Stat(missing.Directory()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection created storage")
	}
	r, err := new(Service).Open(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.RetainTimings(store, history.Limits{}, func(_ string, err error) { t.Error(err) })
	state := r.beginRequest(http.Header{"Thread-Id": {"thread"}})
	state.startTimingAttempt(1, "http", time.Now())
	state.sent(1)
	state.sent(1) // Internal retry with no individually attributable response.
	state.firstOutput(1)
	r.retainTiming(state, time.Now())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	window, _, err := readTimingWindow(store, "thread")
	if err != nil {
		t.Fatal(err)
	}
	attempt := window.Requests[0].Attempts[0]
	if attempt.Sent != nil || attempt.SendObservations != 2 || attempt.Completed != nil {
		t.Fatalf("unpairable evidence became a measurement: %+v", attempt)
	}
	for _, mutation := range []string{"thread", "phase", "version", "outcome", "duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			copy := window
			copy.Requests = append([]RequestTiming(nil), window.Requests...)
			switch mutation {
			case "thread":
				copy.Thread = "another-thread"
			case "version":
				copy.Version++
			case "phase":
				copy.Requests[0].Completed = &TimingPhase{At: time.Now(), OffsetMillis: -1}
			case "outcome":
				copy.Requests[0].Outcome = "private provider error"
			case "duplicate":
				copy.Requests = append(copy.Requests, copy.Requests[0])
			}
			data, err := json.Marshal(&copy)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(store.Directory(), timingName("thread")), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readTimingWindow(store, "thread"); err == nil {
				t.Fatal("invalid evidence accepted by record decoder")
			}
			data, err = new(Service).InspectTiming(t.Context(), store, "thread")
			if err != nil || !strings.Contains(string(data), `"state":"error"`) || strings.Contains(string(data), `"requests"`) {
				t.Fatalf("invalid evidence was not isolated: %s %v", data, err)
			}
		})
	}
}

func TestTimingRetentionFailureIsAuxiliaryAndPrivacyBounded(t *testing.T) {
	_, store := timingStore(t)
	r, err := new(Service).Open(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	notices := 0
	r.RetainTimings(store, history.Limits{Total: 1}, func(_ string, _ error) { notices++ })
	state := r.beginRequest(http.Header{"Thread-Id": {"thread"}, "Authorization": {"Bearer private"}})
	ctx := context.WithValue(t.Context(), captureKey{}, state)
	ObserveRequestOutcome(ctx, "private-error-message")
	r.retainTiming(state, time.Now())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("storage notices=%d", notices)
	}
	if _, found, err := readTimingWindow(store, "thread"); found || err != nil {
		t.Fatalf("failed publication claimed evidence: %v %v", found, err)
	}
	r.RetainTimings(store, history.Limits{}, func(_ string, err error) { t.Error(err) })
	r.retainTiming(state, time.Now())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := new(Service).InspectTiming(t.Context(), store, "thread")
	if err != nil || strings.Contains(string(data), "private") {
		t.Fatalf("private content: %s %v", data, err)
	}
	// Unsafe thread identifiers cannot create an attributable retained record.
	unsafeNotices := 0
	r.RetainTimings(store, history.Limits{}, func(_ string, _ error) { unsafeNotices++ })
	before, _ := os.ReadDir(store.Directory())
	// A neighboring valid observation proves this recorder is actively retaining,
	// rather than making the rejection pass through a closed or disabled writer.
	r.retainTiming(r.beginRequest(http.Header{"Thread-Id": {"thread"}}), time.Now())
	const unsafeThread = "https://private.example/secret"
	r.retainTiming(r.beginRequest(http.Header{"Thread-Id": {unsafeThread}}), time.Now())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := readTimingWindow(store, unsafeThread); err != nil || found || unsafeNotices != 0 {
		t.Fatalf("unsafe identity publication: found=%v notices=%d error=%v", found, unsafeNotices, err)
	}
	if window, found, err := readTimingWindow(store, "thread"); err != nil || !found || len(window.Requests) != 2 {
		t.Fatalf("live retention control: requests=%d found=%v error=%v", len(window.Requests), found, err)
	}
	after, _ := os.ReadDir(store.Directory())
	if len(before) != len(after) {
		t.Fatal(fmt.Sprint("unsafe thread retained: ", after))
	}
}

func TestTimingPublicationDoesNotBlockDeliveryAndBoundsQueue(t *testing.T) {
	_, store := timingStore(t)
	r, err := new(Service).Open(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var notices atomic.Int64
	r.RetainTimings(store, history.Limits{}, func(_ string, _ error) { notices.Add(1) })
	// Keep publication locked while producers complete. They must only enqueue;
	// neither a blocked store nor a full queue may block request delivery.
	err = store.Transaction(t.Context(), nil, func(*history.Transaction) error {
		for range maxPendingTimings + 2 {
			state := r.beginRequest(http.Header{"Thread-Id": {"thread"}})
			r.retainTiming(state, time.Now())
		}
		if len(r.timings.pending) != maxPendingTimings || notices.Load() == 0 {
			t.Fatalf("queue=%d notices=%d", len(r.timings.pending), notices.Load())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	window, _, err := readTimingWindow(store, "thread")
	if err != nil || len(window.Requests) != maxTimingRequests || int(window.Evicted)+len(window.Requests)+int(notices.Load()) != maxPendingTimings+2 {
		t.Fatalf("retained=%d evicted=%d notices=%d error=%v", len(window.Requests), window.Evicted, notices.Load(), err)
	}
}

func TestTimingPrewarmDoesNotCreateRetainedHistory(t *testing.T) {
	_, store := timingStore(t)
	r, err := new(Service).Open(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.RetainTimings(store, history.Limits{}, func(_ string, err error) { t.Error(err) })
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test","generate":false}`))
	request.Header.Set("thread-id", "prewarm")
	r.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ObserveRequestKind(r.Context(), "prewarm")
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(), request)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := readTimingWindow(store, "prewarm"); err != nil || found {
		t.Fatalf("prewarm retained history: %v %v", found, err)
	}
}
