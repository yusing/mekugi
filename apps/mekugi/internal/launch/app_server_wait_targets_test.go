package launch

import (
	"strings"
	"testing"
)

func waitTargetTestAgent(t *testing.T, u *appServerUI, id, path string, running bool) {
	t.Helper()
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": id, "agentNickname": strings.TrimPrefix(path, "/root/")}})
	if running {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": id, "turn": map[string]any{"id": "work"}})
	}
}

func waitTargetTestEvent(t *testing.T, u *appServerUI, method, caller, turn, id string) {
	t.Helper()
	item := coordinationActivityItems(t)[3]
	item.Arguments = []byte(`{"target":["/root/reviewer"]}`)
	item.ID = id
	if method == "item/started" {
		item.Status, item.Result = "inProgress", nil
	}
	appServerTestNotify(t, u, method, map[string]any{"threadId": caller, "turnId": turn, "item": item})
}

func waitTargetTestStore(t *testing.T, u *appServerUI, store *mekugiReplayStore) {
	t.Helper()
	if err := openRetainedSession(u.ctx, u.history, &u.retainedSession, u.Thread, store.publication, store.writes, store.retentionLimits()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		u.retainedSession.Close()
	})
}

func waitTargetTestOpenStore(t *testing.T) *mekugiReplayStore {
	t.Helper()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}
