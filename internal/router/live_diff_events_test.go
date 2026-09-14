package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func liveDiffTestBroker(t *testing.T, store *mekugiReplayStore, scope liveDiffScope) (string, *liveDiffBroker, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	broker := newLiveDiffBroker(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+liveDiffEventsPath, broker.serveEvents)
	mux.HandleFunc("POST "+liveDiffEventsPath+"/producer", broker.serveProducer)
	server := httptest.NewServer(mux)
	t.Cleanup(func() { cancel(); server.Close() })
	broker.setEndpoint(server.URL + liveDiffEventsPath)
	broker.setScope(scope)
	store.liveDiff = broker.publish
	connection := filepath.Join(t.TempDir(), "connection.json")
	data, err := json.Marshal(broker.descriptor())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(connection, data, 0600); err != nil {
		t.Fatal(err)
	}
	return connection, broker, cancel
}

func liveDiffTestSession(t *testing.T, store *mekugiReplayStore, workspace string) string {
	t.Helper()
	path, _, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true, "identity-thread": true, "flush-thread": true}},
	})
	return path
}

func waitLiveDiffChange(t *testing.T, sub *liveDiffSubscriber, confirmed bool) liveDiffChange {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-sub.events:
			if event.Kind == "change" {
				for _, change := range event.Changes {
					for _, call := range change.Change.Calls {
						if call.Confirmed == confirmed {
							return change
						}
					}
				}
			}
		case <-sub.gap:
			t.Fatal("subscriber overflowed")
		case <-timer.C:
			t.Fatal("missing direct publication event")
		}
	}
}

func TestLiveDiffDirectPublicationAndCachedReceipt(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	_, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"identity-thread": true}}})
	sub := broker.subscribe()
	liveDiffIdentityCapture(t, store, workspace, "one", "", "file.txt", "", "original\n", false)
	event := waitLiveDiffChange(t, sub, false)
	data := newLiveDiffData()
	if err := data.apply(t.Context(), store, event); err != nil {
		t.Fatal(err)
	}
	prepared := data.files()
	// Receipts operate on the cached projection, not another disk read.
	callID := event.Change.Calls[0].ID
	if err := os.Remove(filepath.Join(store.directory, replayRecordName(workspace, callID, false))); err != nil {
		t.Fatal(err)
	}
	event.Change.Calls[0].Confirmed = true
	if err := data.apply(t.Context(), store, event); err != nil {
		t.Fatal(err)
	}
	if prepared[0].chunks[0].applied || !strings.Contains(prepared[0].chunks[0].status, "unconfirmed") {
		t.Fatal("receipt mutated the previous display snapshot")
	}
	// An older queued preparation cannot undo a receipt from the snapshot.
	event.Change.Calls[0].Confirmed = false
	if err := data.apply(t.Context(), store, event); err != nil {
		t.Fatal(err)
	}
	files := data.files()
	if len(files) != 1 || !files[0].chunks[0].applied {
		t.Fatalf("cached receipt regressed: %+v", files)
	}
	if _, err := store.liveDiffSnapshot(t.Context(), broker.scope); err == nil {
		t.Fatal("reconnection hid missing durable evidence")
	}
}

func TestLiveDiffScopeEventsRetainMembership(t *testing.T) {
	broker := newLiveDiffBroker(t.Context())
	scope := liveDiffScope{Workspaces: map[string]map[string]bool{"/work": {"root": true}}}
	broker.setScope(scope)
	sub := broker.subscribe()
	initial := <-sub.events
	scope.Workspaces["/work"]["child"] = true
	broker.setScope(scope)
	updated := <-sub.events
	if initial.Scope.Workspaces["/work"]["child"] || !updated.Scope.Workspaces["/work"]["child"] {
		t.Fatal("scope publication changed an already queued membership snapshot")
	}
}

func TestLiveDiffSubscriberOverflowAndIsolation(t *testing.T) {
	broker := newLiveDiffBroker(t.Context())
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{"/work": {"root": true}}})
	sub := broker.subscribe()
	<-sub.events
	broker.publish([]liveDiffChange{{Workspace: "/work", Thread: "other"}})
	if len(sub.events) != 0 {
		t.Fatal("another session leaked into the stream")
	}
	for range 100 {
		broker.publish([]liveDiffChange{{Workspace: "/work", Thread: "root"}})
	}
	select {
	case <-sub.gap:
	default:
		t.Fatal("slow subscriber silently lost events")
	}
	next := broker.subscribe()
	if event := <-next.events; event.Kind != "scope" {
		t.Fatal("reconnection did not establish a snapshot barrier")
	}
}

func TestLiveDiffMixedProducerPublishesEachReceipt(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	_, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	changeID, err := store.reserveChange(t.Context(), workspace, "thread", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	sub := broker.subscribe()
	connection := broker.expectProducer(workspace, "thread", changeID, "Mfixture")
	producer := startLiveDiffProducer(t.Context(), connection)
	defer producer.close()
	state := hpatchResumeState{
		Root: workspace, ReplayDirectory: store.directory, ChangeID: changeID,
		CorrelationID: "mixed", Handle: "Mfixture", liveDiff: producer.publish,
	}
	result, err := state.translateTracked(t.Context(), "new file.txt\ntype \"content\\n\"")
	if err != nil {
		t.Fatal(err)
	}
	prepared := waitLiveDiffChange(t, sub, false)
	if prepared.ID != changeID || prepared.Change.Calls[0].ID != result.AttemptID {
		t.Fatalf("wrong segment event: %+v", prepared)
	}
	if err := state.confirmTracked(t.Context(), result.AttemptID); err != nil {
		t.Fatal(err)
	}
	waitLiveDiffChange(t, sub, true)
}

func waitLiveDiffEvent(t *testing.T, sub *liveDiffSubscriber, match func(liveDiffEvent) bool) liveDiffEvent {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-sub.events:
			if match(event) {
				return event
			}
		case <-sub.gap:
			t.Fatal("unexpected subscriber gap")
		case <-timer.C:
			t.Fatal("missing live diff event")
		}
	}
}

func TestLiveDiffWorkerCoverageLifecycle(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{"/work": {"thread": true}}})
	sub := broker.subscribe()
	<-sub.events
	connection := broker.expectProducer("/work", "thread", "hp_a1", "Mfixture")
	producer := startLiveDiffProducer(t.Context(), connection)
	waitLiveDiffEvent(t, sub, func(e liveDiffEvent) bool { return e.Kind == "coverage" && e.Status == "" })
	producer.close()
	event := waitLiveDiffEvent(t, sub, func(e liveDiffEvent) bool { return e.Kind == "scope" || e.Kind == "coverage" })
	if event.Kind != "coverage" || event.Status != "" {
		t.Fatalf("clean close caused a rescan or coverage loss: %+v", event)
	}
	broker.mu.Lock()
	count := len(broker.producers)
	broker.mu.Unlock()
	if count != 0 {
		t.Fatal("completed workers did not release publisher capacity")
	}
	broker.resumeProducer("/work", "thread", "hp_a1", "Mfixture", connection)
	producer = startLiveDiffProducer(t.Context(), connection)
	defer producer.close()
	waitLiveDiffEvent(t, sub, func(e liveDiffEvent) bool { return e.Kind == "coverage" && e.Status == "" })
	// An abrupt connection close must reconcile possible committed but
	// unannounced records and retain UNAVAILABLE until coverage is restored.
	producer.cancel()
	event = waitLiveDiffEvent(t, sub, func(e liveDiffEvent) bool { return e.Kind == "scope" })
	if !event.Resync || !strings.Contains(event.Status, "UNAVAILABLE") {
		t.Fatalf("worker disconnect was hidden: %+v", event)
	}
	broker.resumeProducer("/work", "thread", "hp_a1", "Mfixture", connection)
	producer = startLiveDiffProducer(t.Context(), connection)
	defer producer.close()
	event = waitLiveDiffEvent(t, sub, func(e liveDiffEvent) bool { return e.Kind == "scope" })
	if !event.Resync || event.Status != "" {
		t.Fatalf("restored worker did not reconcile before claiming coverage: %+v", event)
	}
}

func TestLiveDiffCapacityGapSurvivesScopeAndResume(t *testing.T) {
	broker := newLiveDiffBroker(t.Context())
	broker.setEndpoint("http://127.0.0.1:1234" + liveDiffEventsPath)
	for i := range 256 {
		broker.expectProducer("/work", "thread", "hp_a1", fmt.Sprint(i))
	}
	refused := broker.expectProducer("/work", "thread", "hp_a1", "refused")
	if refused != (liveDiffConnection{}) {
		t.Fatal("capacity was not enforced")
	}
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{"/work": {"thread": true}}})
	clear(broker.producers) // Simulate later capacity becoming available.
	broker.resumeProducer("/work", "thread", "hp_a1", "refused", refused)
	if len(broker.producers) != 0 {
		t.Fatal("resume allocated a descriptor the refused worker cannot read")
	}
	sub := broker.subscribe()
	if event := <-sub.events; !strings.Contains(event.Status, "UNAVAILABLE") {
		t.Fatalf("reconnect hid a refused publisher: %+v", event)
	}
}

func TestLiveDiffScopeReconciliationUsesCachedAttempts(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	scope := liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"identity-thread": true}}}
	liveDiffIdentityCapture(t, store, workspace, "one", "", "file.txt", "", "original\n", false)
	data, err := store.liveDiffSnapshot(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.directory, replayRecordName(workspace, "one", false))); err != nil {
		t.Fatal(err)
	}
	if err := data.reconcile(t.Context(), store, scope); err != nil {
		t.Fatalf("unchanged membership reread an immutable capture: %v", err)
	}
	index, err := store.readChangeIndex(workspace)
	if err != nil {
		t.Fatal(err)
	}
	index.Changes["hp_a1"] = trackedChange{Correlation: "one"}
	if err := store.writeChangeIndex(index); err != nil {
		t.Fatal(err)
	}
	if err := data.reconcile(t.Context(), store, scope); err == nil {
		t.Fatal("reconciliation hid removed membership")
	}
}

func TestLiveDiffWorkerQueueOverflowDoesNotBlock(t *testing.T) {
	canceled := make(chan struct{})
	producer := &liveDiffProducer{
		queue:  make(chan liveDiffProducerMessage, 1),
		cancel: func() { close(canceled) },
	}
	producer.publish([]liveDiffChange{{}})
	producer.publish([]liveDiffChange{{}})
	select {
	case <-canceled:
	default:
		t.Fatal("overflow did not invalidate the publisher connection")
	}
}

func TestLiveDiffStreamReconnectSnapshotBarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("viewer omitted its private capability")
		}
		encoder := json.NewEncoder(w)
		scope := liveDiffScope{Workspaces: map[string]map[string]bool{}}
		_ = encoder.Encode(liveDiffEvent{Kind: "scope", Scope: &scope, Resync: true})
		if requests == 1 {
			_ = encoder.Encode(liveDiffEvent{Kind: "reset"})
		} else {
			_ = encoder.Encode(liveDiffEvent{Kind: "end"})
		}
	}))
	defer server.Close()
	output := make(chan liveDiffEvent, 32)
	liveDiffStream(ctx, liveDiffConnection{Endpoint: server.URL, Token: "test"}, output)
	var kinds []string
	for event := range output {
		kinds = append(kinds, event.Kind)
		if event.Kind == "scope" && !event.Resync {
			t.Fatal("reconnect did not request a fresh snapshot")
		}
	}
	if fmt.Sprint(kinds) != "[scope coverage scope end]" {
		t.Fatalf("reconnect transitions: %v", kinds)
	}
}

func TestLiveDiffPublisherRequiresCapability(t *testing.T) {
	broker := newLiveDiffBroker(t.Context())
	for _, handler := range []http.HandlerFunc{broker.serveEvents, broker.serveProducer} {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}\n")))
		data, _ := io.ReadAll(response.Result().Body)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated connection accepted: %d %s", response.Code, data)
		}
	}
}

func TestLiveDiffJSONBatchKeepsFollowAndRecency(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	_, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {transform.shellThreadID: true}},
	})
	sub := broker.subscribe()
	<-sub.events
	var output []map[string]any
	for _, name := range []string{"first", "second"} {
		output = append(output, map[string]any{
			"type": "custom_tool_call", "id": "item-" + name, "call_id": name,
			"name": mekugiToolName, "input": "new " + name + ".txt\ntype \"content\\n\"",
		})
	}
	if _, _, err := transform.transformResponse(mustMarshalJSON(map[string]any{
		"id": "response", "status": "completed", "output": output,
	}), ""); err != nil {
		t.Fatal(err)
	}
	event := waitLiveDiffEvent(t, sub, func(e liveDiffEvent) bool { return e.Kind == "change" })
	if len(event.Changes) != 2 || len(sub.events) != 0 {
		t.Fatalf("one durable batch was split into display updates: %+v", event)
	}
	data := newLiveDiffData()
	view := liveDiffView{following: true}
	view.merge(nil) // The connected pane starts empty before this batch.
	for _, change := range event.Changes {
		if err := data.apply(t.Context(), store, change); err != nil {
			t.Fatal(err)
		}
	}
	view.merge(data.files())
	view.refreshVisible()
	if view.files[view.selected].path != filepath.Join(workspace, "second.txt") {
		t.Fatal("FOLLOW selected an older call from the durable batch")
	}
	for _, file := range view.files {
		if !view.visible[file.key()].highlighted {
			t.Fatal("durable batch lost a file's recency marker")
		}
	}
	// Replay of that unchanged response does not create another display update.
	if err := transform.commitHistory(); err != nil {
		t.Fatal(err)
	}
	if len(sub.events) != 0 {
		t.Fatal("unchanged replay emitted a new update")
	}
}

func TestLiveDiffDelayedCaptureDoesNotFollowBackwards(t *testing.T) {
	older := liveDiffChunk{key: "older", captureOrder: 1, snapshotOrder: 1}
	newer := liveDiffChunk{key: "newer", captureOrder: 2, snapshotOrder: 2}
	view := liveDiffView{following: true}
	view.merge([]liveDiffFile{{path: "newer.txt", chunks: []liveDiffChunk{newer}}})
	view.merge([]liveDiffFile{
		{path: "older.txt", chunks: []liveDiffChunk{older}},
		{path: "newer.txt", chunks: []liveDiffChunk{newer}},
	})
	if view.latest != "newer" || view.files[view.selected].path != "newer.txt" {
		t.Fatal("late publication of an older capture moved FOLLOW backwards")
	}
}

func TestLiveDiffIdleProducerStopsWithRouter(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, broker, stop := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{}})
	sub := broker.subscribe()
	connection := broker.expectProducer("/work", "thread", "hp_a1", "Mfixture")
	producer := startLiveDiffProducer(t.Context(), connection)
	defer producer.close()
	waitLiveDiffEvent(t, sub, func(e liveDiffEvent) bool { return e.Kind == "coverage" && e.Status == "" })
	stop()
	select {
	case <-producer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle publisher outlived router shutdown")
	}
}

func TestLiveDiffResumeReplacesConnectedControlWorker(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transform.proxy.replayStore = store
	_, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{transform.directory: {transform.shellThreadID: true}},
	})
	transform.proxy.autoLiveDiff = &autoLiveDiff{events: broker}
	transform.proxy.autoLiveDiff.enabled.Store(true)
	id, err := store.reserveChange(t.Context(), transform.directory, transform.shellThreadID, "resume")
	if err != nil {
		t.Fatal(err)
	}
	state, err := transform.retainMixedScript(id, "resume", "shell true", nil)
	if err != nil {
		t.Fatal(err)
	}
	sub := broker.subscribe()
	one, repliesOne, doneOne := startHpatchControlTest(t, transform)
	controlTestRequest(t, one, repliesOne, hpatchControlRequest{Operation: "open", Handle: state.Handle})
	waitLiveDiffEvent(t, sub, func(e liveDiffEvent) bool { return e.Kind == "coverage" && e.Status == "" })

	// A hard-terminated carrier may leave the old native control session alive.
	// Resume translation precedes startControl's close-old/open-new sequence.
	history, err := transform.translateMixedResume("resume-call", "resume "+state.Handle, nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("resume: %v %s", err, history.TranslationError)
	}
	if err := one.Encode(hpatchControlRequest{Operation: "close"}); err != nil {
		t.Fatal(err)
	}
	if err := <-doneOne; err != nil {
		t.Fatal(err)
	}
	broker.mu.Lock()
	status := broker.statusLocked()
	broker.mu.Unlock()
	if !strings.Contains(status, "WAITING") {
		t.Fatalf("old worker close discarded its pending replacement: %q", status)
	}
	two, repliesTwo, doneTwo := startHpatchControlTest(t, transform)
	controlTestRequest(t, two, repliesTwo, hpatchControlRequest{Operation: "open", Handle: state.Handle})
	waitLiveDiffEvent(t, sub, func(e liveDiffEvent) bool { return e.Kind == "coverage" && e.Status == "" })
	response := controlTestRequest(t, two, repliesTwo, hpatchControlRequest{
		Operation: "translate", Source: "new resumed.txt\ntype \"resumed\\n\"",
	})
	event := waitLiveDiffChange(t, sub, false)
	if event.ID != id {
		t.Fatalf("replacement published another operation: %+v", event)
	}
	if err := applyMixedTestPatch(transform.directory, jsonString(response, "patch")); err != nil {
		t.Fatal(err)
	}
	controlTestRequest(t, two, repliesTwo, hpatchControlRequest{
		Operation: "confirm", AttemptID: jsonString(response, "attempt_id"),
	})
	waitLiveDiffChange(t, sub, true)
	if err := two.Encode(hpatchControlRequest{Operation: "close"}); err != nil {
		t.Fatal(err)
	}
	if err := <-doneTwo; err != nil {
		t.Fatal(err)
	}
}
