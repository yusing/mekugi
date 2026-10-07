package router

import (
	"github.com/yusing/mekugi/internal/session"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeSessionSwitchRestoresOwnScope(t *testing.T) {
	s, a, client := runtimeJournalFixture(t)
	runtimeJournalAdd(t, s, a, client, "task-a", "Only A")
	change := runtimeMChangesEdit(t, s, a, "edit-a")
	child := a
	child.Agent = "child-a"
	runtimeJournalBind(t, s, child, client)
	runtimeJournalAdd(t, s, child, client, "child-task", "Only child A")
	read := `{"view":"own"}`
	runtimeJournalReceipt(t, s, a, client, "late-a", "journal_read", read)
	sub := s.owner.broker.subscribe()
	b := a
	b.Session = "session-b"
	b.Workspace = filepath.Join(t.TempDir(), "workspace B with spaces")
	if err := os.Mkdir(b.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	post := func(op string, source, target ObservationBinding, want int) {
		t.Helper()
		if status, body := observationPost(t, s, client, s.Endpoint().Token, observationRequest{Operation: op, Source: source, Binding: target}); status != want {
			t.Fatalf("%s: status=%d body=%q", op, status, body)
		}
	}
	post("session_switch_check", a, b, http.StatusOK)
	if s.owner.session != a.Session || s.journal.root.thread != observationThread(a) {
		t.Fatal("preflight changed the active session")
	}
	post("session_switch", a, b, http.StatusOK)
	select {
	case <-sub.gap:
	default:
		t.Fatal("old display subscription not retired")
	}
	if j := runtimeResetJournal(t, s, b); len(j.Items) != 0 {
		t.Fatal("clear inherited the departing journal")
	}
	if status, _ := observationPost(t, s, client, s.Endpoint().Token, observationRequest{Operation: "bind", Binding: a}); status != http.StatusUnprocessableEntity {
		t.Fatal("old root authorized")
	}
	if status, _ := observationPost(t, s, client, s.Endpoint().Token, observationRequest{Operation: "bind", Binding: child}); status != http.StatusUnprocessableEntity {
		t.Fatal("old child authorized")
	}
	if status, _ := observationPost(t, s, client, s.Endpoint().Token, observationRequest{Operation: "journal_read", NativeID: "late-a", Input: read}); status != http.StatusUnprocessableEntity {
		t.Fatal("old MCP receipt authorized")
	}
	next := s.owner.broker.subscribe()
	if event := <-next.events; event.Scope == nil || len(event.Scope.Workspaces) != 1 || len(event.Scope.Workspaces[b.Workspace]) != 1 || !event.Scope.Workspaces[b.Workspace][observationThread(b)] {
		t.Fatal("clear retained old Diff membership")
	}
	input := runtimeMChangesInput(t, "--mine", "--summary")
	runtimeJournalReceipt(t, s, b, client, "b-changes", "mchanges", input)
	if result := runtimeJournalInvoke(t, s, client, "b-changes", "mchanges", input); strings.Contains(result, "edit-a") {
		t.Fatal("clear borrowed A retained changes")
	}
	runtimeJournalAdd(t, s, b, client, "task-b", "Only B")
	post("session_switch", b, a, http.StatusOK)
	j := runtimeResetJournal(t, s, a)
	if len(j.Items) != 1 || j.Items[0].Title != "Only A" {
		t.Fatalf("resume copied the source journal: %+v", j.Items)
	}
	if s.journal.root.thread != observationThread(a) {
		t.Fatal("resume attached the wrong Journal")
	}
	input = runtimeMChangesInput(t, change, "--summary")
	runtimeJournalReceipt(t, s, a, client, "a-changes", "mchanges", input)
	if result := runtimeJournalInvoke(t, s, client, "a-changes", "mchanges", input); !strings.Contains(result, "edit-a") {
		t.Fatalf("resume lost retained change: %s", result)
	}
	post("session_switch", a, b, http.StatusOK)
	j = runtimeResetJournal(t, s, b)
	if len(j.Items) != 1 || j.Items[0].Title != "Only B" {
		t.Fatal("return to B lost its own journal")
	}
}

func TestRuntimeSessionSwitchRejectsPendingAndInvalidScope(t *testing.T) {
	s, a, client := runtimeJournalFixture(t)
	b := a
	b.Session = "session-b"
	b.Workspace = t.TempDir()
	call := ObservationCall{Binding: a, ID: "unfinished", Tool: "Write", Input: `{}`, Paths: []string{filepath.Join(a.Workspace, "file.txt")}}
	if err := s.owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"session_switch_check", "session_switch"} {
		if status, _ := observationPost(t, s, client, s.Endpoint().Token, observationRequest{Operation: op, Source: a, Binding: b}); status != http.StatusUnprocessableEntity {
			t.Fatal("transition discarded unfinished work")
		}
	}
	nativeObservationWrite(t, call.Paths[0], "native effect\n")
	if _, err := s.owner.after(t.Context(), call, ObservationTerminal{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []ObservationBinding{{}, a, {Runtime: a.Runtime, Workspace: a.Workspace, Session: "b", Agent: "child"}, {Runtime: a.Runtime, Workspace: filepath.Join(t.TempDir(), "missing"), Session: "b"}, {Runtime: a.Runtime, Workspace: call.Paths[0], Session: "b"}} {
		if err := s.switchSession(t.Context(), a, bad, false); err == nil {
			t.Fatal("invalid target accepted")
		}
	}
	wrongSource := a
	wrongSource.Session = "foreign"
	if err := s.switchSession(t.Context(), wrongSource, b, false); err == nil {
		t.Fatal("foreign source accepted")
	}
	if s.owner.session != a.Session || s.owner.pendingCount.Load() != 0 {
		t.Fatal("rejected transition changed source")
	}
}

func TestRuntimeSessionSwitchReplacesSavedDiff(t *testing.T) {
	s, a, _ := runtimeJournalFixture(t)
	runtimeMChangesEdit(t, s, a, "only-a")
	u, _ := runtimeTestUI(t)
	u.thread, u.session.cwd = a.Session, a.Workspace
	u.attachRuntimeObservation(s)
	u.applyRuntimeObservation(<-u.runtime.observationEvents)
	if len(u.shell.diff.data.attempts) != 1 || u.shell.diffFailure != "" {
		t.Fatal("source saved Diff missing")
	}
	b := a
	b.Session = "session-b"
	b.Workspace = t.TempDir()
	if err := s.switchSession(t.Context(), a, b, false); err != nil {
		t.Fatal(err)
	}
	u.runtime.changeRequest, u.runtime.ready = "switch-b", false
	if err := u.runtimeEvent(session.Event{Kind: "session_change", Change: &session.SessionChange{ID: "switch-b", SessionID: b.Session, Cwd: b.Workspace}}); err != nil {
		t.Fatal(err)
	}
	u.applyRuntimeObservation(<-u.runtime.observationEvents)
	if len(u.shell.diff.data.attempts) != 0 || u.shell.diffFailure != "" || len(u.view.entries) != 0 || u.session.cwd != b.Workspace || u.shell.diff.workspace != b.Workspace {
		t.Fatal("target retained the old saved Diff or capture cards")
	}
	if err := s.switchSession(t.Context(), b, a, false); err != nil {
		t.Fatal(err)
	}
	u.runtime.changeRequest = "switch-a"
	if err := u.runtimeEvent(session.Event{Kind: "session_change", Change: &session.SessionChange{ID: "switch-a", SessionID: a.Session, Cwd: a.Workspace}}); err != nil {
		t.Fatal(err)
	}
	u.applyRuntimeObservation(<-u.runtime.observationEvents)
	if len(u.shell.diff.data.attempts) != 1 || u.shell.diffFailure != "" || len(u.view.entries) == 0 || u.session.cwd != a.Workspace || u.shell.diff.workspace != a.Workspace {
		t.Fatal("resume lost its saved Diff and capture cards")
	}
}

func TestRuntimeSessionSwitchStagesWorkspaceCompanion(t *testing.T) {
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	s, a, client := runtimeJournalFixture(t)
	if _, err := s.PrepareCompanion(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtimeJournalBind(t, s, a, client)
	runtimeJournalAdd(t, s, a, client, "saved-a", "Only workspace A")
	originalRegistry, originalPlugin, originalToken := s.registry, s.plugin, s.frontendToken
	originalBinding := runtimeFrontendBinding{Runtime: a.Runtime, Workspace: a.Workspace, Endpoint: ObservationEndpoint{Socket: s.endpoint.Socket, Token: originalToken}}
	b := a
	b.Session, b.Workspace = "session-b", filepath.Join(t.TempDir(), "B with spaces")
	if err := os.Mkdir(b.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.switchSession(t.Context(), a, b, true); err != nil {
		t.Fatal(err)
	}
	if root, err := runtimeFrontendContext(t.Context(), originalBinding); err != nil || root != a || s.registry != originalRegistry || s.plugin != originalPlugin {
		t.Fatalf("preflight retired source context: %+v %v", root, err)
	}
	staged := s.stagedCompanion
	manifest, err := readToolWorkerManifest(filepath.Join(staged.registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil || manifest.Runtime == nil || manifest.Runtime.Workspace != b.Workspace {
		t.Fatalf("target frontends were not authenticated for B: %+v %v", manifest.Runtime, err)
	}
	if err := s.switchSession(t.Context(), a, b, false); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeFrontendContext(t.Context(), originalBinding); err == nil {
		t.Fatal("retired frontend capability borrowed B context")
	}
	for _, path := range []string{originalRegistry.SnapshotDir, originalPlugin} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("drained source artifact remains: %s %v", path, err)
		}
	}
	if root, err := runtimeFrontendContext(t.Context(), *manifest.Runtime); err != nil || root != b {
		t.Fatalf("target frontend context: %+v %v", root, err)
	}
	if j := runtimeResetJournal(t, s, b); len(j.Items) != 0 {
		t.Fatal("target borrowed source journal")
	}
	runtimeJournalAdd(t, s, b, client, "saved-b", "Only workspace B")
	if err := s.switchSession(t.Context(), b, a, false); err != nil {
		t.Fatal(err)
	}
	if j := runtimeResetJournal(t, s, a); len(j.Items) != 1 || j.Items[0].Title != "Only workspace A" {
		t.Fatal("return to A lost its own journal")
	}
	if s.registry == staged.registry || s.plugin == staged.presentation.Plugin || s.stagedCompanion != nil {
		t.Fatal("return to A retained B companion")
	}
	fresh, freshClient, _ := observationIsolationService(t, s.owner.store.directory, b)
	fresh.EnableJournal()
	runtimeJournalBind(t, fresh, b, freshClient)
	if j := runtimeResetJournal(t, fresh, b); len(j.Items) != 1 || j.Items[0].Title != "Only workspace B" {
		t.Fatal("fresh B owner lost its retained Journal or borrowed A")
	}
}

func TestRuntimeSessionSwitchPreflightKeepsSourceOnFailure(t *testing.T) {
	s, a, client := runtimeJournalFixture(t)
	runtimeJournalAdd(t, s, a, client, "source-task", "Keep source")
	b := a
	b.Session, b.Workspace = "session-b", t.TempDir()
	other, _, closeOther := observationIsolationService(t, s.owner.store.directory, b)
	if err := other.owner.bind(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if err := s.switchSession(t.Context(), a, b, true); err == nil {
		t.Fatal("preflight accepted an owned target")
	}
	closeOther()
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	if _, err := s.PrepareCompanion(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtimeJournalBind(t, s, a, client)
	registry := s.registry
	invalidRuntime := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(invalidRuntime, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEKUGI_RUNTIME_DIR", invalidRuntime)
	if err := s.switchSession(t.Context(), a, b, true); err == nil {
		t.Fatal("preflight accepted unavailable frontend storage")
	}
	if s.owner.workspace != a.Workspace || s.owner.session != a.Session || s.registry != registry || s.stagedCompanion != nil {
		t.Fatal("failed preflight changed source ownership")
	}
	if j := runtimeResetJournal(t, s, a); len(j.Items) != 1 || j.Items[0].Title != "Keep source" {
		t.Fatal("failed preflight lost source journal")
	}
}
