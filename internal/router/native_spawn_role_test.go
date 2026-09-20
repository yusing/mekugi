package router

import (
	"encoding/json"
	"testing"
)

func TestNativeSpawnRolesMatchesExactV2CallAndResult(t *testing.T) {
	input := []map[string]any{
		{"type": "function_call", "namespace": "mekugi_collaboration", "name": "spawn_agent", "call_id": "call-1", "arguments": `{"agent_type":"review-correctness","task_name":"review"}`},
		{"type": "function_call_output", "call_id": "call-1", "output": `{"task_name":"/root/review"}`},
		{"type": "function_call", "namespace": "other_mekugi_collaboration", "name": "spawn_agent", "call_id": "call-2", "arguments": `{"agent_type":"worker"}`},
		{"type": "function_call_output", "call_id": "call-2", "output": `{"task_name":"/root/ignored"}`},
		{"type": "function_call", "namespace": "collaboration", "name": "spawn_agent", "call_id": "native-call", "arguments": `{"agent_type":"implementer","task_name":"native"}`},
		{"type": "function_call_output", "call_id": "native-call", "output": `{"task_name":"/root/native"}`},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	got := nativeSpawnRoles(raw, "/root")
	if len(got) != 2 || got["/root/review"] != (journalSpawnRole{Role: "review-correctness"}) || got["/root/native"] != (journalSpawnRole{Role: "implementer"}) {
		t.Fatalf("unexpected roles: %#v", got)
	}
	if inherited := nativeSpawnRoles(raw, "/root/unrelated"); len(inherited) != 0 {
		t.Fatalf("inherited parent history relabeled child: %#v", inherited)
	}
}

func TestNativeSpawnRolesRejectsMissingMalformedAndConflictingEvidence(t *testing.T) {
	input := []map[string]any{
		{"type": "function_call", "namespace": "mekugi_collaboration", "name": "spawn_agent", "call_id": "missing", "arguments": `{"task_name":"missing"}`},
		{"type": "function_call_output", "call_id": "missing", "output": `{"task_name":"/root/missing"}`},
		{"type": "function_call", "namespace": "mekugi_collaboration", "name": "spawn_agent", "call_id": "conflict", "arguments": `{"agent_type":"explorer"}`},
		{"type": "function_call", "namespace": "mekugi_collaboration", "name": "spawn_agent", "call_id": "conflict", "arguments": `{"agent_type":"implementer"}`},
		{"type": "function_call_output", "call_id": "conflict", "output": `{"task_name":"/root/conflict"}`},
		{"type": "function_call_output", "call_id": "unknown", "output": `{"task_name":"/root/unknown"}`},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	got := nativeSpawnRoles(raw, "/root")
	if _, ok := got["/root/missing"]; ok {
		t.Fatalf("missing agent_type inferred a role: %#v", got)
	}
	if role := got["/root/conflict"]; !role.Conflicted || role.Role != "explorer" {
		t.Fatalf("conflicting calls were not retained as conflict: %#v", got)
	}
}

func TestJournalSpawnRolesPersistAcrossRestartAndResolveNestedChildren(t *testing.T) {
	ctx := t.Context()
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := newJournalStore()
	for _, journal := range []struct {
		thread, author, parent string
	}{
		{"root", "/root", ""},
		{"child", "/root/child", "root"},
		{"nested", "/root/child/nested", "child"},
	} {
		if err := store.initialize(ctx, replay, "/workspace", journal.thread, journal.author, ""); err != nil {
			t.Fatal(err)
		}
		if err := store.bindIdentity(ctx, replay, "/workspace", journal.thread, journal.parent, journal.author, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.bindSpawnRoles(ctx, replay, "/workspace", "root", map[string]journalSpawnRole{"/root/child": {Role: "explorer"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.bindSpawnRoles(ctx, replay, "/workspace", "child", map[string]journalSpawnRole{"/root/child/nested": {Role: "review-correctness"}}); err != nil {
		t.Fatal(err)
	}

	store = newJournalStore()
	children, err := store.descendants(replay, "/workspace", "root")
	if err != nil {
		t.Fatal(err)
	}
	roles := make(map[string]string)
	for _, child := range children {
		roles[child.Author] = child.SpawnRole
	}
	if roles["/root/child"] != "explorer" || roles["/root/child/nested"] != "review-correctness" {
		t.Fatalf("roles did not survive restart: %#v", roles)
	}

	if err := store.bindSpawnRoles(ctx, replay, "/workspace", "root", map[string]journalSpawnRole{"/root/child": {Role: "implementer"}}); err != nil {
		t.Fatal(err)
	}
	children, err = store.descendants(replay, "/workspace", "root")
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if child.Author == "/root/child" && child.SpawnRole != "" {
			t.Fatalf("conflicting durable role remained known: %#v", child)
		}
	}
}

func TestForkSpawnBaselineSurvivesRestartAndDoesNotRelabelReusedChild(t *testing.T) {
	ctx := t.Context()
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstInput := mustTestJSON(t, []map[string]any{
		{"type": "function_call", "namespace": "collaboration", "name": "spawn_agent", "call_id": "inherited", "arguments": `{"agent_type":"explorer"}`},
		{"type": "function_call_output", "call_id": "inherited", "output": `{"task_name":"/root/reused"}`},
	})
	store := newJournalStore()
	if err := store.initialize(ctx, replay, "/workspace", "fork", "/root", "source"); err != nil {
		t.Fatal(err)
	}
	if err := store.bindIdentity(ctx, replay, "/workspace", "fork", "", "/root", true); err != nil {
		t.Fatal(err)
	}
	baseline, err := store.forkSpawnBaseline(ctx, replay, "/workspace", "fork", true, nativeSpawnCallIDs(firstInput))
	if err != nil {
		t.Fatal(err)
	}
	if roles := nativeSpawnRoles(firstInput, "/root", baseline); len(roles) != 0 {
		t.Fatalf("first fork request admitted inherited roles: %#v", roles)
	}

	laterInput := mustTestJSON(t, []map[string]any{
		{"type": "function_call", "namespace": "collaboration", "name": "spawn_agent", "call_id": "inherited", "arguments": `{"agent_type":"explorer"}`},
		{"type": "function_call_output", "call_id": "inherited", "output": `{"task_name":"/root/reused"}`},
		{"type": "function_call", "namespace": "mekugi_collaboration", "name": "spawn_agent", "call_id": "new-missing-role", "arguments": `{"task_name":"reused"}`},
		{"type": "function_call_output", "call_id": "new-missing-role", "output": `{"task_name":"/root/reused"}`},
		{"type": "function_call", "namespace": "mekugi_collaboration", "name": "spawn_agent", "call_id": "genuinely-new", "arguments": `{"agent_type":"implementer","task_name":"fresh"}`},
		{"type": "function_call_output", "call_id": "genuinely-new", "output": `{"task_name":"/root/fresh"}`},
	})
	store = newJournalStore()
	baseline, err = store.forkSpawnBaseline(ctx, replay, "/workspace", "fork", true, nativeSpawnCallIDs(laterInput))
	if err != nil {
		t.Fatal(err)
	}
	if !baseline["inherited"] || baseline["new-missing-role"] || baseline["genuinely-new"] {
		t.Fatalf("restart changed immutable fork baseline: %#v", baseline)
	}
	roles := nativeSpawnRoles(laterInput, "/root", baseline)
	if len(roles) != 1 || roles["/root/fresh"] != (journalSpawnRole{Role: "implementer"}) {
		t.Fatalf("fork baseline blocked new evidence or inherited role relabeled reused child: %#v", roles)
	}
	if err := store.initialize(ctx, replay, "/workspace", "reused", "/root/reused", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.bindIdentity(ctx, replay, "/workspace", "reused", "fork", "/root/reused", true); err != nil {
		t.Fatal(err)
	}
	children, err := store.descendants(replay, "/workspace", "fork")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].SpawnRole != "" {
		t.Fatalf("reused child acquired inherited role: %#v", children)
	}
}
