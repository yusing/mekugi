package router

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeObservationFailedBindDoesNotAuthorizeCalls(t *testing.T) {
	service, binding, client := observationHTTPFixture(t)
	blocker := filepath.Join(service.owner.store.directory, changeIndexName(binding.Workspace, observationThread(binding)))
	if err := os.Mkdir(blocker, 0700); err != nil {
		t.Fatal(err)
	}
	post := func(request observationRequest, want int) {
		t.Helper()
		if status, body := observationPost(t, service, client, service.Endpoint().Token, request); status != want {
			t.Fatalf("%s: status=%d want=%d body=%q", request.Operation, status, want, body)
		}
	}
	post(observationRequest{Operation: "bind", Binding: binding}, http.StatusUnprocessableEntity)
	call := ObservationCall{Binding: binding, ID: "unbound", Tool: "Write", Input: `{}`, Paths: []string{"file.txt"}}
	post(observationRequest{Operation: "before", Call: call}, http.StatusUnprocessableEntity)
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	post(observationRequest{Operation: "bind", Binding: binding}, http.StatusOK)
	sub := service.owner.broker.subscribe()
	defer service.owner.broker.unsubscribe(sub)
	if event := <-sub.events; !event.Scope.Workspaces[binding.Workspace][observationThread(binding)] {
		t.Fatal("successful bind retry did not publish saved Diff scope")
	}
	post(observationRequest{Operation: "before", Call: call}, http.StatusOK)
}

func TestNativeObservationLatePreToolDoesNotReopenSettledCall(t *testing.T) {
	for _, terminalStatus := range []string{"completed", "failed", "stopped"} {
		t.Run(terminalStatus, func(t *testing.T) {
			service, binding, client := observationHTTPFixture(t)
			post := func(request observationRequest, want int) {
				t.Helper()
				if status, body := observationPost(t, service, client, service.Endpoint().Token, request); status != want {
					t.Fatalf("%s: status=%d want=%d body=%q", request.Operation, status, want, body)
				}
			}
			post(observationRequest{Operation: "bind", Binding: binding}, http.StatusOK)
			call := ObservationCall{Binding: binding, ID: "terminal-first", Tool: "Write", Input: `{}`, Paths: []string{"late.txt"}}
			nativeObservationWrite(t, filepath.Join(binding.Workspace, "late.txt"), "already executed\n")
			post(observationRequest{Operation: "after", Call: call, Terminal: ObservationTerminal{Status: terminalStatus}}, http.StatusOK)
			history := nativeObservationHistory(t, service.owner.store, call, "after")
			if history.ChangeID != "" || history.ExecOutcome.Coverage != execCoverageUnswept {
				t.Fatal("missing pre-tool evidence became a capture")
			}
			post(observationRequest{Operation: "before", Call: call}, http.StatusOK)
			if service.owner.pendingCount.Load() != 0 {
				t.Fatal("late pre-tool receipt reopened a settled observation")
			}
			if _, found, err := service.owner.store.lookup(t.Context(), binding.Workspace, observationKey(call)+"/before"); err != nil || found {
				t.Fatalf("late pre-tool captured a post-effect baseline: found=%v err=%v", found, err)
			}
			changed := call
			changed.Input = `{"changed":true}`
			post(observationRequest{Operation: "before", Call: changed}, http.StatusUnprocessableEntity)
			if replay := nativeObservationHistory(t, service.owner.store, call, "after"); !reflect.DeepEqual(history, replay) {
				t.Fatal("late pre-tool changed settled evidence")
			}
		})
	}
}

// Each phase runs the actual service and saved-Diff consumer in a separate Go
// process. No in-memory store, lease, broker or task mapping crosses this edge.
func TestNativeObservationFreshProcessRecovery(t *testing.T) {
	if phase := os.Getenv("MEKUGI_OBSERVATION_RECOVERY_PHASE"); phase != "" {
		nativeObservationRecoveryPhase(t, phase, os.Getenv("MEKUGI_OBSERVATION_RECOVERY_DIR"))
		return
	}
	t.Parallel()
	directory := t.TempDir()
	workspace := filepath.Join(directory, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"record", "resume"} {
		if phase == "resume" {
			for _, name := range []string{"root.txt", "child.txt", "pending.txt", "child-pending.txt"} {
				nativeObservationWrite(t, filepath.Join(workspace, name), "disconnected unrelated writer\n")
			}
		}
		cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestNativeObservationFreshProcessRecovery$", "-test.count=1")
		cmd.Env = append(os.Environ(), "MEKUGI_OBSERVATION_RECOVERY_PHASE="+phase, "MEKUGI_OBSERVATION_RECOVERY_DIR="+directory)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fresh process %s: %v\n%s", phase, err, output)
		}
	}
}

func nativeObservationRecoveryPhase(t *testing.T, phase, directory string) {
	t.Helper()
	workspace := filepath.Join(directory, "workspace")
	root := ObservationBinding{Runtime: "claude", Session: "resumed-native-session", Workspace: workspace}
	service, client, _ := observationIsolationService(t, filepath.Join(directory, "store"), root)
	owner, store := service.owner, service.owner.store
	post := func(request observationRequest, want int) {
		t.Helper()
		if status, body := observationPost(t, service, client, service.Endpoint().Token, request); status != want {
			t.Fatalf("%s: status=%d want=%d body=%q", request.Operation, status, want, body)
		}
	}
	child := root
	child.Agent = "native-child"
	post(observationRequest{Operation: "bind", Binding: root}, http.StatusOK)
	calls := []ObservationCall{
		{Binding: root, ID: "root", Tool: "Bash", Input: `{}`, Command: "printf effect > root.txt", Shell: "bash"},
		{Binding: child, ID: "child", Tool: "Write", Input: `{}`, Paths: []string{"child.txt"}},
		{Binding: root, ID: "pending", Tool: "Bash", Input: `{}`, Command: "printf later > pending.txt", Shell: "bash"},
		{Binding: child, ID: "child-pending", Tool: "Bash", Input: `{}`, Command: "printf later > child-pending.txt", Shell: "bash"},
	}
	if phase == "record" {
		post(observationRequest{Operation: "bind", Binding: child}, http.StatusOK)
		for i, call := range calls {
			nativeObservationWrite(t, filepath.Join(workspace, call.ID+".txt"), "baseline\n")
			post(observationRequest{Operation: "before", Call: call}, http.StatusOK)
			post(observationRequest{Operation: "after", Call: call, Terminal: ObservationTerminal{Task: call.ID + "-task", Status: "running"}}, http.StatusOK)
			if i < 2 {
				nativeObservationWrite(t, filepath.Join(workspace, call.ID+".txt"), "retained effect\n")
				post(observationRequest{Operation: "task", Task: observationTask{Session: root.Session, ID: call.ID + "-task", CallID: call.ID, Status: "completed"}}, http.StatusOK)
			}
		}
		return
	}
	if phase != "resume" {
		t.Fatalf("unexpected recovery phase %q", phase)
	}

	// Retained mapping is evidence, not a live task or permission to call as child.
	for _, call := range calls {
		record := nativeObservationHistory(t, store, call, "background")
		if record.NativeObservation == nil || !reflect.DeepEqual(record.NativeObservation.Call, &call) || record.NativeObservation.Terminal == nil || record.NativeObservation.Terminal.Task != call.ID+"-task" {
			t.Fatalf("task/call correlation lost: %#v", record.NativeObservation)
		}
	}
	if owner.pendingCount.Load() != 0 || len(owner.live) != 0 || len(owner.tasks) != 0 || len(owner.taskEvents) != 0 {
		t.Fatal("restart revived process-local observations")
	}
	for _, call := range []ObservationCall{calls[1], calls[3]} {
		post(observationRequest{Operation: "before", Call: call}, http.StatusUnprocessableEntity)
		post(observationRequest{Operation: "after", Call: call, Terminal: ObservationTerminal{Status: "completed"}}, http.StatusUnprocessableEntity)
	}
	fork := root
	fork.Branch = "unverified-native-fork"
	post(observationRequest{Operation: "bind", Binding: fork}, http.StatusUnprocessableEntity)

	sub := owner.broker.subscribe()
	defer owner.broker.unsubscribe(sub)
	event := <-sub.events
	if event.Kind != "scope" || !event.Resync || len(event.Scope.Workspaces) != 1 || len(event.Scope.Workspaces[workspace]) != 2 || !event.Scope.Workspaces[workspace][observationThread(child)] {
		t.Fatalf("saved membership not recovered: %#v", event)
	}
	if len(owner.broker.takePreviews(sub)) != 0 {
		t.Fatal("restart restored ephemeral previews")
	}
	saved, err := store.liveDiffSnapshot(t.Context(), *event.Scope)
	if err != nil {
		t.Fatal(err)
	}
	files := saved.files()
	if len(files) != 2 {
		t.Fatalf("saved Diff files=%d want=2", len(files))
	}
	var previousOrder uint64
	for _, call := range calls[:2] {
		history := nativeObservationHistory(t, store, call, "after")
		found := false
		for _, file := range files {
			if file.Path != filepath.Join(workspace, call.ID+".txt") {
				continue
			}
			found = true
			if len(file.Chunks) != 1 || file.Chunks[0].Change != history.ChangeID || history.ChangeID == "" || file.Chunks[0].CaptureOrder <= previousOrder || !strings.Contains(file.Chunks[0].Review.Diff, "-baseline\n+retained effect\n") {
				t.Fatalf("saved capture lost identity/content: %#v", file)
			}
			previousOrder = file.Chunks[0].CaptureOrder
		}
		if !found {
			t.Fatalf("missing saved file for %s", call.ID)
		}
	}

	// Replayed settled hook and SDK receipts never allocate or reread later bytes.
	settled := nativeObservationHistory(t, store, calls[0], "after")
	post(observationRequest{Operation: "before", Call: calls[0]}, http.StatusOK)
	post(observationRequest{Operation: "after", Call: calls[0], Terminal: ObservationTerminal{Task: "root-task", Status: "running"}}, http.StatusOK)
	post(observationRequest{Operation: "task", Task: observationTask{Session: root.Session, ID: "root-task", CallID: "root", Status: "completed"}}, http.StatusOK)
	if replay := nativeObservationHistory(t, store, calls[0], "after"); !reflect.DeepEqual(settled, replay) {
		t.Fatal("duplicate receipt changed retained capture")
	}
	// No recoverable terminal boundary is inferred for a disconnected task.
	post(observationRequest{Operation: "task", Task: observationTask{Session: root.Session, ID: "pending-task", CallID: "pending", Status: "completed"}}, http.StatusOK)
	if _, found, err := store.lookup(t.Context(), workspace, observationKey(calls[2])+"/after"); err != nil || found {
		t.Fatalf("task notification revived a disconnected window: found=%v err=%v", found, err)
	}
	post(observationRequest{Operation: "before", Call: calls[2]}, http.StatusOK)
	post(observationRequest{Operation: "after", Call: calls[2], Terminal: ObservationTerminal{Task: "pending-task", Status: "completed"}}, http.StatusOK)
	gap := nativeObservationHistory(t, store, calls[2], "after")
	if gap.ChangeID != "" || len(gap.ReviewFiles) != 0 || gap.ExecOutcome == nil || gap.ExecOutcome.Coverage != execCoverageUnswept || owner.pendingCount.Load() != 0 {
		t.Fatalf("disconnected interval became authored evidence: %#v", gap)
	}
	after, err := store.liveDiffSnapshot(t.Context(), *event.Scope)
	if err != nil || !reflect.DeepEqual(files, after.files()) {
		t.Fatalf("replayed receipts changed saved Diff: %v", err)
	}
}
