package router

import (
	json "encoding/json/v2"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
	"github.com/yusing/mekugi/internal/vcsguard"
)

func TestNativeRuntimeVCSGuardAuthenticatedInput(t *testing.T) {
	t.Parallel()
	s, binding, http := observationHTTPFixture(t)
	helper, err := execTrackHelper()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PrepareCommandTracking(t.Context(), helper, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareVCSGuard(t.Context(), helper); err != nil {
		t.Fatal(err)
	}
	if err = s.owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	call := ObservationCall{Binding: binding, ID: "bash", Tool: "Bash", Command: "false && git push; command git status", Input: `{"command":"false && git push; command git status","timeout":1000,"description":"keep","future":9007199254740993}`}
	status, text := observationPost(t, s, http, s.endpoint.Token, observationRequest{Operation: "before", Call: call})
	var result struct {
		GuardReady   bool   `json:"guardReady"`
		CaptureError string `json:"captureError"`
	}
	if status != 200 || json.Unmarshal([]byte(text), &result) != nil || result.CaptureError != "" || !result.GuardReady {
		t.Fatalf("guard response %d %s", status, text)
	}
	script, err := s.guardScript(call)
	baseline := nativeObservationHistory(t, s.owner.store, call, "before")
	if err != nil || !sameObservationCall(baseline.NativeObservation.Call, &call) {
		t.Fatalf("guard changed native arguments: %+v %v", baseline.NativeObservation.Call, err)
	}
	if got := vcsguard.DisplayScript(script, func(directory string) bool { return directory == s.guard.directory }); got != call.Command {
		t.Fatalf("guard source changed: %s", got)
	}
	status, text = observationPost(t, s, http, s.endpoint.Token, observationRequest{Operation: "after", Call: call, Terminal: ObservationTerminal{Status: "failed", Report: "native denial"}})
	if status != 200 || s.owner.pendingCount.Load() != 0 {
		t.Fatalf("native tuple settlement %d %s", status, text)
	}
	foreign := call
	foreign.Binding.Session = "foreign"
	status, _ = observationPost(t, s, http, s.endpoint.Token, observationRequest{Operation: "before", Call: foreign})
	if status != 422 {
		t.Fatalf("foreign guard admitted: %d", status)
	}
}

func TestUISnapshotNativeRuntimeVCSGuard(t *testing.T) {
	u, s, binding := observedRuntimeUIFixture(t)
	s.guard = &nativeVCSGuard{directory: "/private/vcs-guard"}
	u.thread = binding.Session
	u.runtimeEvent(session.Event{Kind: "tool", ID: "write", Role: "Bash", Text: `{"command":"git status; git push origin main"}`})
	item, _ := json.Marshal(nativeGuardItem{Binding: binding, ID: "write"})
	request := &vcsApproval{thread: "foreign-codex", item: string(item), cwd: binding.Workspace, argv: []string{"git", "push", "origin", "main"}, executable: "/usr/bin/git", reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})}
	u.runtime.permissionChoices = map[string]string{"write": "Approved"}
	u.runtimeGuardApproval(request)
	now := u.now()
	u.clock = func() time.Time { return now.Add(time.Second) }
	if request.thread != binding.Session || request.item != "write" {
		t.Fatal("borrowed foreign host identity")
	}
	runtimePermissionState(t, u, "write", "Pending Approval\nCommand: git push origin main")
	uisnapshot.Assert(t, "testdata/snapshots/native-runtime-vcs-guard.txt", runtimeFrame(t, u, 80, 20))
	runtimeKeys(t, u, "3\r")
	if reply := <-request.reply; reply.OK {
		t.Fatal("shared dock failed to deny")
	}
	runtimePermissionState(t, u, "write", "Denied\nCommand: git push origin main")
	u.confirmRuntimePermission("write")
	runtimePermissionState(t, u, "write", "Denied\nCommand: git push origin main")
}
