package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAutoLiveDiffEnableAndScopeEligibility(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("PATH", t.TempDir())

	for _, test := range []struct {
		name       string
		enabled    bool
		workspace  string
		thread     string
		metadata   codexTurnMetadata
		wantScoped bool
	}{
		{name: "disabled", workspace: t.TempDir(), thread: "root", metadata: codexTurnMetadata{RequestKind: "turn"}},
		{name: "root turn", enabled: true, workspace: t.TempDir(), thread: "root", metadata: codexTurnMetadata{RequestKind: "turn"}, wantScoped: true},
		{name: "child turn is scoped", enabled: true, workspace: t.TempDir(), thread: "child", metadata: codexTurnMetadata{RequestKind: "turn", SubagentKind: "review"}, wantScoped: true},
		{name: "auxiliary request", enabled: true, workspace: t.TempDir(), thread: "root", metadata: codexTurnMetadata{RequestKind: "compact"}},
		{name: "invalid activity identity", enabled: true, workspace: t.TempDir(), thread: "root", metadata: codexTurnMetadata{RequestKind: "turn", activityIdentityInvalid: true}},
		{name: "empty workspace", enabled: true, thread: "root", metadata: codexTurnMetadata{RequestKind: "turn"}},
		{name: "empty thread", enabled: true, workspace: t.TempDir(), metadata: codexTurnMetadata{RequestKind: "turn"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, stop := newAutoLiveDiff(t.Context(), t.TempDir())
			defer stop()
			if test.enabled {
				a.enable()
			}
			a.observe(test.workspace, test.thread, test.metadata)
			scoped := a.scope.Workspaces[test.workspace][test.thread]
			if scoped != test.wantScoped {
				t.Fatalf("thread scope = %v, want %v", scoped, test.wantScoped)
			}
			wantRoot := ""
			if test.wantScoped && test.metadata.SubagentKind == "" {
				wantRoot = test.workspace
			}
			if a.workspace != wantRoot {
				t.Fatalf("root workspace = %q, want %q", a.workspace, wantRoot)
			}
			if a.requested {
				t.Fatal("observing a turn requested the integrated UI")
			}
			select {
			case <-a.changed:
			default:
			}
		})
	}
}

func TestAutoLiveDiffRequestsOnlyAnEligibleScopedShell(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("PATH", t.TempDir())
	a, stop := newAutoLiveDiff(t.Context(), t.TempDir())
	defer stop()
	a.enable()
	rootWorkspace, childWorkspace := t.TempDir(), t.TempDir()
	a.observe(childWorkspace, "child", codexTurnMetadata{RequestKind: "turn", SubagentKind: "review"})
	if a.workspace != "" {
		t.Fatalf("child selected root workspace %q", a.workspace)
	}
	a.observe(rootWorkspace, "root", codexTurnMetadata{RequestKind: "turn"})
	if a.workspace != rootWorkspace {
		t.Fatalf("root workspace = %q, want %q", a.workspace, rootWorkspace)
	}
	// Consume observation notifications so only an eligible shell can satisfy
	// the request assertions below.
	select {
	case <-a.changed:
	default:
	}

	a.requestLaunch(childWorkspace, "unknown")
	if a.requested {
		t.Fatal("unobserved thread requested UI")
	}
	a.requestLaunch(t.TempDir(), "root")
	if a.requested {
		t.Fatal("thread from another workspace requested UI")
	}
	select {
	case <-a.changed:
		t.Fatal("ineligible shell emitted a UI change")
	default:
	}

	a.requestLaunch(rootWorkspace, "root")
	if !a.requested {
		t.Fatal("eligible shell did not set the one-shot UI request")
	}
	select {
	case <-a.changed:
	default:
		t.Fatal("eligible shell did not notify the integrated UI")
	}
	a.requestLaunch(rootWorkspace, "root")
	select {
	case <-a.changed:
		t.Fatal("repeated shell request notified twice")
	default:
	}
}

func TestAutoLiveDiffChildShellWaitsForRootWorkspace(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("PATH", t.TempDir())
	a, stop := newAutoLiveDiff(t.Context(), t.TempDir())
	defer stop()
	a.enable()
	childWorkspace, rootWorkspace := t.TempDir(), t.TempDir()
	a.observe(childWorkspace, "child", codexTurnMetadata{RequestKind: "turn", SubagentKind: "review"})
	a.requestLaunch(childWorkspace, "child")
	if !a.requested || a.workspace != "" {
		t.Fatalf("child request did not defer until a root turn: requested=%v workspace=%q", a.requested, a.workspace)
	}
	a.observe(rootWorkspace, "root", codexTurnMetadata{RequestKind: "turn"})
	if a.workspace != rootWorkspace {
		t.Fatalf("root workspace = %q, want %q", a.workspace, rootWorkspace)
	}
}

func TestAutoLiveDiffActivityRequestLifecycle(t *testing.T) {
	a, stop := newAutoLiveDiff(t.Context(), t.TempDir())
	defer stop()
	if a.requestActivity() {
		t.Fatal("disabled integrated UI accepted agents pane request")
	}
	a.enable()
	if a.requestActivity() {
		t.Fatal("agents pane request succeeded before root workspace selection")
	}
	workspace := t.TempDir()
	a.observe(workspace, "root", codexTurnMetadata{RequestKind: "turn"})
	select {
	case <-a.changed:
	default:
		t.Fatal("root observation did not notify the integrated UI")
	}
	if !a.requestActivity() || !a.activityRequested {
		t.Fatal("agents pane request was not recorded")
	}
	select {
	case <-a.changed:
	default:
		t.Fatal("agents pane request did not notify the integrated UI")
	}
	if a.requestActivity() {
		t.Fatal("agents pane request was accepted twice")
	}
	stop()
	if a.enabled.Load() || !a.stopped {
		t.Fatalf("stop state: enabled=%v stopped=%v", a.enabled.Load(), a.stopped)
	}
	if a.requestActivity() {
		t.Fatal("agents pane request was accepted after UI shutdown")
	}
	a.requestLaunch(workspace, "root")
	if a.requested {
		t.Fatal("shell request was accepted after UI shutdown")
	}
}

func TestAutoLiveDiffScopeCapacity(t *testing.T) {
	// This fixture exercises scope accounting without starting the integrated UI.
	a := &autoLiveDiff{
		events:     newLiveDiffBroker(t.Context()),
		scope:      liveDiffScope{Workspaces: make(map[string]map[string]bool)},
		scopeBytes: len(`{"Workspaces":{}}`),
		changed:    make(chan struct{}, 1),
	}
	a.enabled.Store(true)
	for _, workspace := range []string{"/tmp/a", "/tmp/with\"quote"} {
		for _, thread := range []string{"root", "child\nwith\\escape"} {
			a.observe(workspace, thread, codexTurnMetadata{RequestKind: "turn"})
			data, err := json.Marshal(a.scope)
			if err != nil || len(data) != a.scopeBytes {
				t.Fatalf("scope accounting: %d != %d (%v)", len(data), a.scopeBytes, err)
			}
		}
	}
	a.observe("/tmp/oversized", strings.Repeat("x", maxLiveDiffScopeBytes), codexTurnMetadata{RequestKind: "turn"})
	if a.enabled.Load() || a.scope.Workspaces != nil {
		t.Fatal("scope exhaustion did not disable/release auxiliary collection")
	}
	a.observe("/tmp/after", "new", codexTurnMetadata{RequestKind: "turn"})
	if a.scope.Workspaces != nil {
		t.Fatal("disabled collection resumed")
	}
}
