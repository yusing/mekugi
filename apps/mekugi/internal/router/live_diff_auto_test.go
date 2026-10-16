package router

import (
	"testing"
)

func TestSavedReviewScopeEligibility(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("PATH", t.TempDir())

	for _, test := range []struct {
		name       string
		workspace  string
		thread     string
		metadata   codexTurnMetadata
		wantScoped bool
	}{
		{name: "root turn", workspace: t.TempDir(), thread: "root", metadata: codexTurnMetadata{RequestKind: "turn"}, wantScoped: true},
		{name: "child turn is scoped", workspace: t.TempDir(), thread: "child", metadata: codexTurnMetadata{RequestKind: "turn", SubagentKind: "review"}, wantScoped: true},
		{name: "auxiliary request", workspace: t.TempDir(), thread: "root", metadata: codexTurnMetadata{RequestKind: "compact"}},
		{name: "invalid activity identity", workspace: t.TempDir(), thread: "root", metadata: codexTurnMetadata{RequestKind: "turn", activityIdentityInvalid: true}},
		{name: "empty workspace", thread: "root", metadata: codexTurnMetadata{RequestKind: "turn"}},
		{name: "empty thread", workspace: t.TempDir(), metadata: codexTurnMetadata{RequestKind: "turn"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := newSavedReview(t.Context(), nil)
			observeReview(a, test.workspace, test.thread, test.metadata)
			scoped := a.Scope().Workspaces[test.workspace][test.thread]
			if scoped != test.wantScoped {
				t.Fatalf("thread scope = %v, want %v", scoped, test.wantScoped)
			}
			wantRoot := ""
			if test.wantScoped && test.metadata.SubagentKind == "" {
				wantRoot = test.workspace
			}
			if a.Workspace() != wantRoot {
				t.Fatalf("root workspace = %q, want %q", a.Workspace(), wantRoot)
			}
			select {
			case <-a.Changed():
			default:
			}
		})
	}
}
