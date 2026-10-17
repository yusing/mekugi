package router

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestSavedReviewScopeEligibility(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	t.Setenv("PATH", t.TempDir())

	for _, test := range []struct {
		name       string
		workspace  string
		thread     string
		metadata   codexresponses.TurnMetadata
		wantScoped bool
	}{
		{name: "root turn", workspace: t.TempDir(), thread: "root", metadata: codexresponses.TurnMetadata{RequestKind: "turn"}, wantScoped: true},
		{name: "child turn is scoped", workspace: t.TempDir(), thread: "child", metadata: codexresponses.TurnMetadata{RequestKind: "turn", SubagentKind: "review"}, wantScoped: true},
		{name: "auxiliary request", workspace: t.TempDir(), thread: "root", metadata: codexresponses.TurnMetadata{RequestKind: "compact"}},
		{name: "invalid activity identity", workspace: t.TempDir(), thread: "root", metadata: codexresponses.TurnMetadata{RequestKind: "turn", ActivityIdentityInvalid: true}},
		{name: "empty workspace", thread: "root", metadata: codexresponses.TurnMetadata{RequestKind: "turn"}},
		{name: "empty thread", workspace: t.TempDir(), metadata: codexresponses.TurnMetadata{RequestKind: "turn"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := sessionreview.New(t.Context(), nil, nil)
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
