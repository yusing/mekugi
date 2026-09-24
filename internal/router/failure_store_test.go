package router

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestFailureReferencePersistsWithoutDebugAndResolvesAfterReopen(t *testing.T) {
	directory := t.TempDir()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}

	// No debug output is configured. The ordinary replay store retains the error.
	issues := NewCriticalErrors()
	issues.failureStore = store
	const secret = "Authorization Bearer failure-store-secret"
	const prompt = "private request body that must not be retained"
	wrapped := fmt.Errorf("translate prompt %q: %w", prompt,
		criticalDiagnostic(errors.New(secret), "mekugi_sse:invalid_json", "the response could not be translated", false))
	failure := &requestFinalization{
		sessionID: "routing-session", threadID: "thread-failure-store",
		failurePhase: requestFailureTransform,
		streamDiagnostics: &streamDiagnostics{
			Transport: "http", BodyBytes: 128, Events: 2, LastEvent: "response.created",
			CopyStop: "translation_error",
		},
	}
	if err := failure.finish(t.Context(), wrapped, &bytes.Buffer{}, issues); err != nil {
		t.Fatalf("finish failed: %v", err)
	}
	if failure.diagnosticReference == "" {
		t.Fatal("failed request did not produce a diagnostic reference")
	}

	files, err := failureRecordFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("persisted failure files = %d, want 1", len(files))
	}

	var record failureRecord
	if err := json.Unmarshal(files[0], &record); err != nil {
		t.Fatal(err)
	}
	if record.Version != 1 || record.Thread != failure.threadID || record.Phase != requestFailureTransform ||
		record.Code != "mekugi_sse:invalid_json" || record.Reference != failure.diagnosticReference || record.Time.IsZero() ||
		record.Error != wrapped.Error() || !strings.Contains(record.Error, secret) || !strings.Contains(record.Error, prompt) {
		t.Fatalf("persisted failure metadata = %+v", record)
	}
	var stream map[string]any
	if err := json.Unmarshal(record.Stream, &stream); err != nil {
		t.Fatal(err)
	}
	if stream["transport"] != "http" || stream["last_event"] != "response.created" {
		t.Fatalf("safe stream diagnostics were not retained: %s", record.Stream)
	}

	// Reopening the directory models a fresh router/store instance: lookup must
	// rely on retained files, not the original CriticalErrors or store object.
	if _, err := openMekugiReplayStore(directory); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	status := RunSessionInspection(t.Context(), []string{
		"--failures", "--replay-dir", directory, failure.diagnosticReference,
	}, &stdout, &stderr)
	if status != 0 {
		t.Fatalf("inspect-session --failures REF status=%d stderr=%q", status, stderr.String())
	}
	var resolved []failureRecord
	if err := json.Unmarshal(stdout.Bytes(), &resolved); err != nil {
		t.Fatalf("resolve output is not JSON: %s: %v", stdout.String(), err)
	}
	if len(resolved) != 1 || resolved[0].Reference != failure.diagnosticReference {
		t.Fatalf("resolved failures = %+v", resolved)
	}

	stdout.Reset()
	stderr.Reset()
	status = RunSessionInspection(t.Context(), []string{"--failures", "--replay-dir", directory}, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 {
		t.Fatalf("inspect-session --failures status=%d stderr=%q", status, stderr.String())
	}
	var listed []failureRecord
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0].Reference != failure.diagnosticReference {
		t.Fatalf("listed failures = %s, err=%v", stdout.String(), err)
	}
}

func TestFailureRetentionExpiresInactiveButProtectsActiveSession(t *testing.T) {
	directory := t.TempDir()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	const expiredThread = "expired-failure-thread"
	const activeThread = "active-failure-thread"
	for _, thread := range []string{expiredThread, activeThread} {
		ctx, release, err := store.beginSession(t.Context(), thread, thread)
		if err != nil {
			t.Fatal(err)
		}
		record := failureRecord{
			Version: 1, Time: time.Now().UTC(), Thread: thread,
			Phase: requestFailureForward, Code: "upstream_transport_unknown",
			Reference: "reference-" + thread,
		}
		if err := store.scoped(ctx).appendFailure(t.Context(), record); err != nil {
			release()
			t.Fatal(err)
		}
		if thread == expiredThread {
			release()
		} else {
			t.Cleanup(release)
		}
	}
	retentionTestAge(t, store, expiredThread, sessionRetention+time.Hour)
	retentionTestAge(t, store, activeThread, sessionRetention+time.Hour)
	current, releaseCurrent := retentionTestSession(t, store, "retention-sweep-current", 0)
	defer releaseCurrent()
	if err := store.cleanupSessions(current); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := inspectFailures(t.Context(), directory, "", &output); err != nil {
		t.Fatal(err)
	}
	var retained []failureRecord
	if err := json.Unmarshal(output.Bytes(), &retained); err != nil {
		t.Fatal(err)
	}
	if len(retained) != 1 || retained[0].Reference != "reference-"+activeThread {
		t.Fatalf("retained failures = %+v, want only active thread %q", retained, activeThread)
	}
	if err := inspectFailures(t.Context(), directory, "reference-"+expiredThread, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "may have expired") {
		t.Fatalf("expired reference error = %v", err)
	}
}

func TestInspectFailuresRejectsSessionInspectionOptionCombinations(t *testing.T) {
	directory := t.TempDir()
	tests := []struct {
		name  string
		extra []string
	}{
		{name: "session", extra: []string{"--session", "rollout.jsonl"}},
		{name: "workspace", extra: []string{"--workspace", t.TempDir()}},
		{name: "ax", extra: []string{"--ax"}},
		{name: "read log", extra: []string{"--read-log", "reads.jsonl"}},
		{name: "defects", extra: []string{"--defects", "defects.json"}},
		{name: "field", extra: []string{"--field", "all"}},
		{name: "call id", extra: []string{"--call-id", "call-1"}},
		{name: "multiple references", extra: []string{"first-ref", "second-ref"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--failures", "--replay-dir", directory}, test.extra...)
			var stdout, stderr bytes.Buffer
			status := RunSessionInspection(t.Context(), args, &stdout, &stderr)
			if status != 2 {
				t.Fatalf("status=%d, want usage error 2; stdout=%q stderr=%q", status, stdout.String(), stderr.String())
			}
		})
	}
}

func TestFailurePersistenceLazilyOpensStateDirectory(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	issues := NewCriticalErrors()
	issues.persistFailures = true
	failure := failureStoreTestFinalization("lazy-open-reference")

	issues.persistFailure(failure)

	issues.mu.Lock()
	store := issues.failureStore
	issues.mu.Unlock()
	if store == nil {
		t.Fatal("failure persistence did not lazily open the configured state directory")
	}
	files, err := failureRecordFiles(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("persisted failure files = %d, want 1", len(files))
	}
	var record failureRecord
	if err := json.Unmarshal(files[0], &record); err != nil {
		t.Fatal(err)
	}
	if record.Reference != failure.diagnosticReference {
		t.Fatalf("persisted reference = %q, want %q", record.Reference, failure.diagnosticReference)
	}
}

func TestFailurePersistenceOpenFailureRetainsOnlyNotice(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(stateHome, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", stateHome)
	issues := NewCriticalErrors()
	issues.persistFailures = true
	failure := failureStoreTestFinalization("failed-open-reference")

	issues.persistFailure(failure)

	issues.mu.Lock()
	store := issues.failureStore
	issues.mu.Unlock()
	if store != nil {
		t.Fatal("failed state-directory open was cached as a usable failure store")
	}
	if pending := strings.Join(issues.Pending(), "\n"); !strings.Contains(pending, "could not retain the failure reference") {
		t.Fatalf("failure-store notice missing: %s", pending)
	}
	if files, err := failureRecordFiles(filepath.Join(stateHome, "mekugi", "replay")); err != nil || len(files) != 0 {
		t.Fatalf("failed open left failure records: files=%d err=%v", len(files), err)
	}
}

func TestFailureStoreLockTimeoutDoesNotBlockCriticalNoticeAccess(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	directory, err := defaultMekugiReplayDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openMekugiReplayStore(directory); err != nil {
		t.Fatal(err)
	}
	storeLock := flock.New(filepath.Join(directory, "store.lock"), flock.SetPermissions(0600))
	locked, err := storeLock.TryLock()
	if err != nil || !locked {
		t.Fatalf("could not hold managed store lock: locked=%v err=%v", locked, err)
	}

	issues := NewCriticalErrors()
	issues.persistFailures = true
	failure := failureStoreTestFinalization("lock-timeout-reference")
	done := make(chan struct{})
	started := time.Now()
	persistDeadline := time.After(5 * time.Second)
	go func() {
		issues.persistFailure(failure)
		close(done)
	}()
	defer func() {
		_ = storeLock.Unlock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("failure persistence goroutine did not stop after releasing store.lock")
		}
	}()

	// Let lazy open reach the held store lock before exercising the mutex-backed
	// notice API. The persistence operation has a two-second internal deadline.
	time.Sleep(100 * time.Millisecond)
	accessed := make(chan struct{})
	go func() {
		issues.addNotice("session", "concurrent-access", "notice access remained available")
		_ = issues.Pending()
		close(accessed)
	}()
	select {
	case <-accessed:
	case <-time.After(750 * time.Millisecond):
		t.Fatal("notice access blocked while failure-store initialization waited for store.lock")
	}

	select {
	case <-done:
	case <-persistDeadline:
		t.Fatal("failure persistence did not return within its bounded timeout")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("failure persistence exceeded timeout headroom: %s", elapsed)
	}
	pending := strings.Join(issues.Pending(), "\n")
	if !strings.Contains(pending, "could not retain the failure reference") {
		t.Fatalf("timed-out persistence did not report degradation: %s", pending)
	}
	if files, err := failureRecordFiles(directory); err != nil || len(files) != 0 {
		t.Fatalf("timed-out persistence published failure records: files=%d err=%v", len(files), err)
	}
}

func failureStoreTestFinalization(reference string) *requestFinalization {
	return &requestFinalization{
		sessionID: "failure-store-test-session", threadID: "failure-store-test-thread",
		failurePhase: requestFailureForward, diagnosticCode: "upstream_transport_unknown",
		diagnosticReference: reference,
	}
}

func failureRecordFiles(directory string) ([][]byte, error) {
	entries, err := filepath.Glob(directory + "/failure-*.json")
	if err != nil {
		return nil, err
	}
	files := make([][]byte, 0, len(entries))
	for _, name := range entries {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		files = append(files, data)
	}
	return files, nil
}
