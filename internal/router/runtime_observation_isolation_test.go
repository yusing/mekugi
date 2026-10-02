package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func observationIsolationService(t *testing.T, directory string, binding ObservationBinding) (*ObservationService, *http.Client, func()) {
	t.Helper()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newNativeObservationOwner(t.Context(), store, binding.Runtime, binding.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	service, err := startObservationService(owner)
	if err != nil {
		t.Fatal(err)
	}
	// Closing explicitly for restart also satisfies the cleanup below.
	closeService := sync.OnceFunc(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(closeService)
	endpoint := service.Endpoint()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", endpoint.Socket)
	}}}
	t.Cleanup(client.CloseIdleConnections)
	return service, client, closeService
}

func TestNativeObservationIPCConcurrentIsolation(t *testing.T) {
	for _, sharedWorkspace := range []bool{true, false} {
		name := "different-workspaces/same-session"
		if sharedWorkspace {
			name = "same-workspace/different-sessions"
		}
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			workspace := t.TempDir()
			bindings := []ObservationBinding{
				{Runtime: "claude", Session: "session-a", Workspace: workspace},
				{Runtime: "claude", Session: "session-a", Workspace: t.TempDir()},
			}
			if sharedWorkspace {
				bindings[1].Workspace = workspace
				bindings[1].Session = "session-b"
			}
			services := make([]*ObservationService, 2)
			clients := make([]*http.Client, 2)
			closers := make([]func(), 2)
			calls := make([]ObservationCall, 2)
			subs := make([]*liveDiffSubscriber, 2)
			scopes := make([]liveDiffScope, 2)
			post := func(i int, request observationRequest, want int) {
				t.Helper()
				status, body := observationPost(t, services[i], clients[i], services[i].Endpoint().Token, request)
				if status != want || want == http.StatusOK && body != "{}" {
					t.Errorf("service %d %s: status=%d body=%q, want %d", i, request.Operation, status, body, want)
				}
			}
			for i, binding := range bindings {
				services[i], clients[i], closers[i] = observationIsolationService(t, directory, binding)
				post(i, observationRequest{Operation: "bind", Binding: binding}, http.StatusOK)
				path := "file.txt"
				if sharedWorkspace {
					path = fmt.Sprintf("file-%d.txt", i)
				}
				calls[i] = ObservationCall{Binding: binding, ID: "identical-tool-id", Tool: "Write", Input: `{}`, Paths: []string{path}}
				nativeObservationWrite(t, filepath.Join(binding.Workspace, path), fmt.Sprintf("before-%d\n", i))
				subs[i] = services[i].owner.broker.subscribe()
				event := <-subs[i].events
				scopes[i] = liveDiffScope{Workspaces: map[string]map[string]bool{binding.Workspace: {observationThread(binding): true}}}
				if event.Kind != "scope" || !reflect.DeepEqual(event.Scope, &scopes[i]) {
					t.Fatalf("service %d membership leaked: %#v", i, event)
				}
			}
			if services[0].owner.store == services[1].owner.store {
				t.Fatal("fixture reused the same store object")
			}
			for i := range services {
				foreign := bindings[1-i]
				post(i, observationRequest{Operation: "bind", Binding: foreign}, http.StatusUnprocessableEntity)
				post(i, observationRequest{Operation: "before", Call: calls[1-i]}, http.StatusUnprocessableEntity)
				post(i, observationRequest{Operation: "after", Call: calls[1-i], Terminal: ObservationTerminal{Status: "completed"}}, http.StatusUnprocessableEntity)
				foreignSession := foreign.Session
				if foreignSession == bindings[i].Session {
					foreignSession = "unbound-session"
				}
				post(i, observationRequest{Operation: "task", Task: observationTask{ID: "identical-task-id", CallID: calls[i].ID, Session: foreignSession, Status: "completed"}}, http.StatusUnprocessableEntity)
			}
			// Release both services' duplicate IPC requests together, rather than
			// testing idempotence through serial owner-method calls.
			concurrent := func(requests func(int) []observationRequest) {
				t.Helper()
				start := make(chan struct{})
				var wg sync.WaitGroup
				for i := range services {
					for _, request := range requests(i) {
						wg.Go(func() {
							<-start
							post(i, request, http.StatusOK)
						})
					}
				}
				close(start)
				wg.Wait()
			}
			concurrent(func(i int) []observationRequest {
				r := observationRequest{Operation: "before", Call: calls[i]}
				return []observationRequest{r, r, r}
			})
			for i, call := range calls {
				nativeObservationWrite(t, filepath.Join(call.Binding.Workspace, call.Paths[0]), fmt.Sprintf("after-%d\n", i))
			}
			concurrent(func(i int) []observationRequest {
				r := observationRequest{Operation: "after", Call: calls[i], Terminal: ObservationTerminal{Task: "identical-task-id", Status: "running"}}
				return []observationRequest{r, r, r}
			})
			for i, call := range calls {
				if _, found, err := services[i].owner.store.lookup(t.Context(), call.Binding.Workspace, observationKey(call)+"/after"); err != nil || found {
					t.Fatalf("service %d running return finalized: found=%v err=%v", i, found, err)
				}
			}
			complete := func(i int) []observationRequest {
				task := observationRequest{Operation: "task", Task: observationTask{ID: "identical-task-id", CallID: calls[i].ID, Session: bindings[i].Session, Status: "completed", Report: "native completion"}}
				hook := observationRequest{Operation: "after", Call: calls[i], Terminal: ObservationTerminal{Task: "identical-task-id", Status: "completed", Report: "native completion"}}
				return []observationRequest{task, task, hook, hook}
			}
			concurrent(complete)
			reader, err := openMekugiReplayStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			histories := make([]mekugiHistory, 2)
			for i, call := range calls {
				histories[i] = nativeObservationHistory(t, reader, call, "after")
				history := histories[i]
				path := filepath.Join(call.Binding.Workspace, call.Paths[0])
				if history.ChangeID == "" || history.ExecOutcome == nil || history.ExecOutcome.Status != "completed" || len(history.ReviewFiles) != 1 || history.ReviewFiles[0].AfterPath != path || !strings.Contains(history.ReviewFiles[0].Diff, fmt.Sprintf("-before-%d\n+after-%d\n", i, i)) {
					t.Fatalf("service %d actual effect/identity lost: %#v", i, history)
				}
				if services[i].owner.pendingCount.Load() != 0 {
					t.Fatalf("service %d retained pending work", i)
				}
				nativeObservationWrite(t, path, "unrelated later effect\n")
			}
			concurrent(func(i int) []observationRequest {
				return append(complete(i), observationRequest{Operation: "before", Call: calls[i]})
			})
			for i, call := range calls {
				if replay := nativeObservationHistory(t, reader, call, "after"); !reflect.DeepEqual(replay, histories[i]) {
					t.Fatalf("service %d duplicate receipts changed immutable evidence", i)
				}
				files, err := reader.liveDiffSnapshotFiles(t.Context(), scopes[i])
				if err != nil || len(files) != 1 || files[0].Path != filepath.Join(call.Binding.Workspace, call.Paths[0]) || len(files[0].Chunks) != 1 {
					t.Fatalf("service %d saved Diff duplicated or leaked: %#v err=%v", i, files, err)
				}
				published := 0
			drain:
				for {
					select {
					case <-subs[i].gap:
						t.Fatal("Diff subscriber overflowed")
					case event := <-subs[i].events:
						for _, change := range event.Changes {
							published++
							if change.Workspace != call.Binding.Workspace || change.Thread != observationThread(call.Binding) || change.ID != histories[i].ChangeID {
								t.Fatalf("service %d foreign Diff publication: %#v", i, change)
							}
						}
					default:
						break drain
					}
				}
				if published == 0 {
					t.Fatalf("service %d capture not published", i)
				}
			}
			for _, closeService := range closers {
				closeService()
			}
			// Fresh owners recover saved membership independently from the shared
			// directory, without borrowing a sibling service's live scope.
			for i, binding := range bindings {
				services[i], clients[i], closers[i] = observationIsolationService(t, directory, binding)
				post(i, observationRequest{Operation: "bind", Binding: binding}, http.StatusOK)
				event := <-services[i].owner.broker.subscribe().events
				if event.Kind != "scope" || !reflect.DeepEqual(event.Scope, &scopes[i]) {
					t.Fatalf("service %d restored foreign membership: %#v", i, event)
				}
				files, err := services[i].owner.store.liveDiffSnapshotFiles(t.Context(), *event.Scope)
				if err != nil || len(files) != 1 || files[0].Path != filepath.Join(binding.Workspace, calls[i].Paths[0]) || len(files[0].Chunks) != 1 {
					t.Fatalf("service %d restored Diff duplicated or leaked: %#v err=%v", i, files, err)
				}
			}
		})
	}
}
