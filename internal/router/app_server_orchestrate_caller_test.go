package router

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAppServerOrchestrateFirstChildMCPOwnership(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := orchestrateIdentityPendingTurnWithReplay(t, replay)
	// The initial turn is only requested; no child execution has established cwd.
	_, workspace, _, release, err := journalMCPContext(t.Context(), u.proxy, mcp.Meta{"threadId": "child", "sessionId": "child-session"})
	if err != nil {
		t.Fatal("first child MCP call lacks owned workspace:", err)
	}
	defer release()
	if workspace != u.orchestrateThreads["child"].batch.Cwd {
		t.Fatal("first child caller inherited coordinator cwd", workspace)
	}
}
