package router

import (
	"io"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotNativeRosterDurableRoles(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := newJournalStore()
	workspace := t.TempDir()
	for _, j := range []struct{ thread, author, parent string }{
		{"main", "/root", ""}, {"review", "/root/review", "main"}, {"nested", "/root/review/probe", "review"}, {"other", "/root/other", ""},
	} {
		if err := store.initialize(t.Context(), replay, workspace, j.thread, j.author, ""); err != nil {
			t.Fatal(err)
		}
		if err := store.bindIdentity(t.Context(), replay, workspace, j.thread, j.parent, j.author, true); err != nil {
			t.Fatal(err)
		}
	}
	for thread, roles := range map[string]map[string]journalSpawnRole{
		"main":   {"/root/investigate": {Role: "investigator"}, "/root/tests": {Role: "worker"}, "/root/review": {Role: "review-correctness"}, "/root/simplify": {Role: "review-simplify"}, "/root/unknown": {Conflicted: true}},
		"review": {"/root/review/probe": {Role: "explorer"}},
		"other":  {"/root/unknown": {Role: "council-member"}},
	} {
		if err := store.bindSpawnRoles(t.Context(), replay, workspace, thread, roles); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := openMekugiReplayStore(replay.directory)
	if err != nil {
		t.Fatal(err)
	}
	store = newJournalStore()
	u := newAppServerSessionTestUI(t, workspace)
	u.journal = store.attachNative(workspace, "main")
	if err := store.restoreNative(t.Context(), reopened, u.journal); err != nil {
		t.Fatal(err)
	}
	for _, info := range []appServerThreadInfo{
		{ID: "investigate", AgentNickname: "investigate"}, {ID: "tests", AgentNickname: "tests"}, {ID: "review", AgentNickname: "review"}, {ID: "simplify", AgentNickname: "simplify"},
		{ID: "nested", Source: []byte(`{"subAgent":{"thread_spawn":{"agent_path":"/root/review/probe"}}}`)},
		{ID: "unknown", AgentNickname: "unknown"},
	} {
		u.session.registerThread(info)
	}
	for i := range u.session.agents {
		u.session.agents[i].Final = true
	}
	u.restoreHistory(nil)
	u.agents.painter.Theme = livediff.DarkTheme
	u.agents.selected = "/root/review"
	if err := u.paint(io.Discard, 180, 40); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	assertNativeUISnapshot(t, "native-roster-durable-roles", u.agents.nativeRoster(180, 10, now, true))
	if u.session.agent("/root/review").Role != "" {
		t.Fatal("display fallback changed host metadata")
	}
	if u.agents.agents[len(u.agents.agents)-1].Role != "" {
		t.Fatal("conflicting or unrelated role leaked")
	}
	// Conflicting evidence must remove a previously displayed fallback role.
	if err := store.bindSpawnRoles(t.Context(), reopened, workspace, "main", map[string]journalSpawnRole{"/root/review": {Role: "worker"}}); err != nil {
		t.Fatal(err)
	}
	u.refreshRosterRoles()
	if got := u.agents.agents[3].Role; got != "" {
		t.Fatalf("conflicted role remained: %q", got)
	}
	// Host-supplied roles remain authoritative when spawn evidence conflicts.
	u.session.registerThread(appServerThreadInfo{ID: "review", AgentRole: "review-correctness"})
	u.refreshRosterRoles()
	if got := u.agents.agents[3].Role; got != "review-correctness" {
		t.Fatalf("host role lost: %q", got)
	}
}
