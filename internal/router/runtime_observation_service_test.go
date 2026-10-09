package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func observationHTTPFixture(t *testing.T) (*ObservationService, ObservationBinding, *http.Client) {
	t.Helper()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newNativeObservationOwner(t.Context(), store, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	service, err := startObservationService(owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	endpoint := service.Endpoint()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", endpoint.Socket)
	}}}
	t.Cleanup(client.CloseIdleConnections)
	return service, ObservationBinding{Runtime: "claude", Session: "session", Workspace: workspace}, client
}

func TestNativeObservationChildContextBoundary(t *testing.T) {
	service, root, client := observationHTTPFixture(t)
	child := root
	child.Agent = "native-child"
	for _, binding := range []ObservationBinding{root, child} {
		if err := service.owner.bind(t.Context(), binding); err != nil {
			t.Fatal(err)
		}
	}
	foreign := child
	foreign.Session = "retired-session"
	unknown := child
	unknown.Agent = "unbound-child"
	for _, item := range []struct {
		binding ObservationBinding
		status  int
	}{{child, http.StatusOK}, {foreign, http.StatusUnprocessableEntity}, {unknown, http.StatusUnprocessableEntity}} {
		status, body := observationPost(t, service, client, service.Endpoint().Token, observationRequest{Operation: "context_boundary", Binding: item.binding})
		if status != item.status {
			t.Fatalf("native child boundary status=%d want=%d: %s", status, item.status, body)
		}
	}
}
func observationPost(t *testing.T, service *ObservationService, client *http.Client, token string, value any) (int, string) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://local/observe", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
}
func TestNativeObservationServiceCapabilityAndCapture(t *testing.T) {
	service, binding, client := observationHTTPFixture(t)
	token := service.Endpoint().Token
	payload := observationRequest{Operation: "bind", Binding: binding}
	if status, _ := observationPost(t, service, client, "wrong", payload); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated bind: %d", status)
	}
	if status, _ := observationPost(t, service, client, token, map[string]any{"operation": "bind", "binding": binding, "unexpected": true}); status != http.StatusBadRequest {
		t.Fatalf("unknown fields accepted: %d", status)
	}
	if status, body := observationPost(t, service, client, token, payload); status != http.StatusOK || body != "{}" {
		t.Fatalf("bind: %d %q", status, body)
	}
	call := ObservationCall{Binding: binding, ID: "tool", Tool: "Write", Input: `{"file_path":"file.txt","content":"after"}`, Paths: []string{"file.txt"}}
	for _, request := range []observationRequest{{Operation: "before", Call: call}} {
		if status, body := observationPost(t, service, client, token, request); status != http.StatusOK {
			t.Fatalf("before: %d %q", status, body)
		}
	}
	nativeObservationWrite(t, filepath.Join(binding.Workspace, "file.txt"), "after\n")
	if status, body := observationPost(t, service, client, token, observationRequest{Operation: "after", Call: call, Terminal: ObservationTerminal{Status: "failed", Report: "host failure"}}); status != http.StatusOK || body != "{}" {
		t.Fatalf("after: %d %q", status, body)
	}
	history := nativeObservationHistory(t, service.owner.store, call, "after")
	if history.ChangeID == "" || len(history.ReviewFiles) != 1 || history.ExecOutcome.Exit != nil {
		t.Fatalf("actual failure effect lost: %#v", history)
	}
}
func TestNativeObservationBackgroundTerminalEdges(t *testing.T) {
	t.Parallel()
	for _, early := range []bool{false, true} {
		for _, status := range []string{"completed", "failed", "stopped"} {
			t.Run(status+map[bool]string{true: "/early", false: "/late"}[early], func(t *testing.T) {
				owner, store, binding := nativeObservationFixture(t)
				call := ObservationCall{Binding: binding, ID: "background", Tool: "Bash", Input: `{}`, Command: "printf before > background.txt", Shell: "bash"}
				if err := owner.before(t.Context(), call); err != nil {
					t.Fatal(err)
				}
				event := observationTask{ID: "native-task", CallID: call.ID, Session: binding.Session, Status: status, Report: "native terminal"}
				if early {
					if err := owner.task(t.Context(), event); err != nil {
						t.Fatal(err)
					}
				}
				nativeObservationWrite(t, filepath.Join(binding.Workspace, "background.txt"), "actual\n")
				if _, err := owner.after(t.Context(), call, ObservationTerminal{Task: event.ID, Status: "running"}); err != nil {
					t.Fatal(err)
				}
				if !early {
					if _, found, err := store.lookup(t.Context(), binding.Workspace, observationKey(call)+"/after"); err != nil || found {
						t.Fatalf("immediate background return finalized: %v %v", found, err)
					}
					wrong := event
					wrong.CallID = "different"
					if err := owner.task(t.Context(), wrong); err == nil {
						t.Fatal("mismatched background call accepted")
					}
					if err := owner.task(t.Context(), event); err != nil {
						t.Fatal(err)
					}
				}
				history := nativeObservationHistory(t, store, call, "after")
				if history.ChangeID == "" || history.ExecOutcome.Status != status || history.ExecOutcome.Exit != nil || owner.pendingCount.Load() != 0 {
					t.Fatalf("terminal edge: %#v pending=%d", history, owner.pendingCount.Load())
				}
				if err := owner.task(t.Context(), event); err != nil {
					t.Fatal(err)
				}
				duplicate := nativeObservationHistory(t, store, call, "after")
				if duplicate.ChangeID != history.ChangeID {
					t.Fatal("terminal replay allocated another change")
				}
			})
		}
	}
}
func TestNativeObservationOverlappingNamedCallsComposeOnce(t *testing.T) {
	owner, store, binding := nativeObservationFixture(t)
	path := filepath.Join(binding.Workspace, "same.txt")
	nativeObservationWrite(t, path, "old\n")
	a := ObservationCall{Binding: binding, ID: "a", Tool: "Edit", Input: `{}`, Paths: []string{path}}
	b := a
	b.ID = "b"
	for _, call := range []ObservationCall{a, b} {
		if err := owner.before(t.Context(), call); err != nil {
			t.Fatal(err)
		}
	}
	nativeObservationWrite(t, path, "first effect\n")
	if id, err := owner.after(t.Context(), a, ObservationTerminal{Status: "completed"}); err != nil || id == "" {
		t.Fatalf("first: %q %v", id, err)
	}
	if id, err := owner.after(t.Context(), b, ObservationTerminal{Status: "completed"}); err != nil || id != "" {
		t.Fatalf("overlap double captured: %q %v", id, err)
	}
	history := nativeObservationHistory(t, store, a, "after")
	if len(history.ExecOutcome.Overlaps) == 0 {
		t.Fatal("overlap uncertainty omitted")
	}
}
func TestNativeObservationSessionLeaseAndSavedChildRestoration(t *testing.T) {
	owner, store, binding := nativeObservationFixture(t)
	second, err := newNativeObservationOwner(t.Context(), store, "claude", binding.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	if err := second.bind(t.Context(), binding); err == nil {
		t.Fatal("two owners bound the same session")
	}
	child := binding
	child.Agent = "native-child"
	if err := owner.bind(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	call := ObservationCall{Binding: child, ID: "child-write", Tool: "Write", Input: `{}`, Paths: []string{"child.txt"}}
	if err := owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	nativeObservationWrite(t, filepath.Join(binding.Workspace, "child.txt"), "child\n")
	if _, err := owner.after(t.Context(), call, ObservationTerminal{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	owner.close()
	if err := second.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	sub := second.broker.subscribe()
	event := <-sub.events
	if !event.Scope.Workspaces[binding.Workspace][observationThread(child)] {
		t.Fatal("saved child membership lost on root resume")
	}
	if err := second.before(t.Context(), call); err == nil {
		t.Fatal("saved child membership became live caller authority")
	}
}
func TestNativeObservationServiceRemovesOnlyOwnedIPC(t *testing.T) {
	service, _, _ := observationHTTPFixture(t)
	endpoint := service.Endpoint()
	if info, err := os.Stat(filepath.Dir(endpoint.Socket)); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("private directory: %v %v", info, err)
	}
	// This case uses a second service so the fixture cleanup remains single-shot.
	owner, _, _ := nativeObservationFixture(t)
	second, err := startObservationService(owner)
	if err != nil {
		t.Fatal(err)
	}
	socket := second.Endpoint().Socket
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatal("owned socket remains")
	}
	if _, err := os.Stat(service.owner.store.directory); err != nil {
		t.Fatal("unrelated retained evidence removed")
	}
}

func TestNativeObservationCaptureTimeoutHasNoFalseRecord(t *testing.T) {
	owner, store, binding := nativeObservationFixture(t)
	for range cap(execCaptureSlots) {
		execCaptureSlots <- struct{}{}
	}
	defer func() {
		for range cap(execCaptureSlots) {
			<-execCaptureSlots
		}
	}()
	call := ObservationCall{Binding: binding, ID: "timeout", Tool: "Write", Input: `{}`, Paths: []string{"late.txt"}}
	if err := owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	nativeObservationWrite(t, filepath.Join(binding.Workspace, "late.txt"), "actual but unavailable baseline\n")
	if id, err := owner.after(t.Context(), call, ObservationTerminal{Status: "completed"}); err != nil || id != "" {
		t.Fatalf("timeout fabricated durable change: %q %v", id, err)
	}
	history := nativeObservationHistory(t, store, call, "after")
	if history.ExecOutcome.Coverage == execCoverageExact || len(history.ReviewFiles) != 0 {
		t.Fatal("timeout claimed exact no-effect capture")
	}
}

func TestNativeObservationMixedScopeOverlap(t *testing.T) {
	for _, namedFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "named-first", false: "snapshot-first"}[namedFirst], func(t *testing.T) {
			owner, store, b := nativeObservationFixture(t)
			path := filepath.Join(b.Workspace, "same.txt")
			nativeObservationWrite(t, path, "old\n")
			named := ObservationCall{Binding: b, ID: "named", Tool: "Edit", Input: `{}`, Paths: []string{path}}
			opaque := ObservationCall{Binding: b, ID: "opaque", Tool: "Bash", Input: `{}`, Command: "python script.py", Shell: "bash"}
			for _, c := range []ObservationCall{named, opaque} {
				if err := owner.before(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			base := nativeObservationHistory(t, store, opaque, "before")
			if base.ExecObservation.Tree == "" {
				t.Fatal("missing snapshot fixture")
			}
			if len(base.ExecObservation.Files) != 0 {
				t.Fatalf("expected opaque scope: %#v", base.ExecObservation)
			}
			nativeObservationWrite(t, path, "new\n")
			first, second := named, opaque
			if !namedFirst {
				first, second = opaque, named
			}
			id1, err := owner.after(t.Context(), first, ObservationTerminal{Status: "completed"})
			if err != nil || id1 == "" {
				t.Fatalf("first %q %v", id1, err)
			}
			id2, err := owner.after(t.Context(), second, ObservationTerminal{Status: "completed"})
			if err != nil {
				t.Fatal(err)
			}
			if id2 != "" {
				h := nativeObservationHistory(t, store, second, "after")
				t.Fatalf("duplicate effect allocated %s after %s: %#v", id2, id1, h.ReviewFiles)
			}
		})
	}
}

func TestNativeObservationEarlyTaskPersistenceRetry(t *testing.T) {
	owner, store, b := nativeObservationFixture(t)
	call := ObservationCall{Binding: b, ID: "bg", Tool: "Write", Input: `{}`, Paths: []string{"bg.txt"}}
	if err := owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	nativeObservationWrite(t, filepath.Join(b.Workspace, "bg.txt"), "new\n")
	event := observationTask{Session: b.Session, ID: "task", CallID: call.ID, Status: "completed"}
	if err := owner.task(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(store.directory, "capture-order")
	if err := os.Mkdir(blocker, 0700); err != nil {
		t.Fatal(err)
	}
	terminal := ObservationTerminal{Task: event.ID, Status: "running"}
	if _, err := owner.after(t.Context(), call, terminal); err == nil {
		t.Fatal("expected allocation failure")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.after(t.Context(), call, terminal); err != nil {
		t.Fatal(err)
	}
	if owner.pendingCount.Load() != 0 {
		t.Fatalf("retry acknowledged but capture remains pending: %d", owner.pendingCount.Load())
	}
}

func TestNativeObservationMixedScopeRestorationComposes(t *testing.T) {
	owner, store, binding := nativeObservationFixture(t)
	path := filepath.Join(binding.Workspace, "same.txt")
	nativeObservationWrite(t, path, "original\n")
	named := ObservationCall{Binding: binding, ID: "named", Tool: "Edit", Input: `{}`, Paths: []string{path}}
	opaque := ObservationCall{Binding: binding, ID: "opaque", Tool: "Bash", Input: `{}`, Command: "python writer.py", Shell: "bash"}
	for _, call := range []ObservationCall{named, opaque} {
		if err := owner.before(t.Context(), call); err != nil {
			t.Fatal(err)
		}
	}
	nativeObservationWrite(t, path, "first effect\n")
	first, err := owner.after(t.Context(), named, ObservationTerminal{Status: "failed"})
	if err != nil || first == "" {
		t.Fatalf("first: %q %v", first, err)
	}
	nativeObservationWrite(t, path, "original\n")
	second, err := owner.after(t.Context(), opaque, ObservationTerminal{Status: "completed"})
	if err != nil || second == "" || second == first {
		t.Fatalf("restoration: %q %v", second, err)
	}
	history := nativeObservationHistory(t, store, opaque, "after")
	if len(history.ReviewFiles) != 1 || !strings.Contains(history.ReviewFiles[0].Diff, "-first effect") || !strings.Contains(history.ReviewFiles[0].Diff, "+original") {
		t.Fatal("original snapshot equality erased the later restoration")
	}
}

func TestNativeObservationSnapshotClaimsDoNotBroadenNamedScope(t *testing.T) {
	owner, store, binding := nativeObservationFixture(t)
	namedPath := filepath.Join(binding.Workspace, "named.txt")
	otherPath := filepath.Join(binding.Workspace, "other.txt")
	nativeObservationWrite(t, namedPath, "named original\n")
	nativeObservationWrite(t, otherPath, "other original\n")
	named := ObservationCall{Binding: binding, ID: "named", Tool: "Edit", Input: `{}`, Paths: []string{namedPath}}
	opaque := ObservationCall{Binding: binding, ID: "opaque", Tool: "Bash", Input: `{}`, Command: "python writer.py", Shell: "bash"}
	for _, call := range []ObservationCall{named, opaque} {
		if err := owner.before(t.Context(), call); err != nil {
			t.Fatal(err)
		}
	}
	nativeObservationWrite(t, otherPath, "observed opaque effect\n")
	if _, err := owner.after(t.Context(), opaque, ObservationTerminal{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	nativeObservationWrite(t, otherPath, "later unrelated writer\n")
	nativeObservationWrite(t, namedPath, "named effect\n")
	if _, err := owner.after(t.Context(), named, ObservationTerminal{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	history := nativeObservationHistory(t, store, named, "after")
	if len(history.ReviewFiles) != 1 || history.ReviewFiles[0].AfterPath != namedPath {
		t.Fatalf("snapshot claims broadened named scope: %#v", history.ReviewFiles)
	}
}
