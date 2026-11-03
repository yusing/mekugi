package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type uniqueChildren struct {
	instructionNative
	created *int
}

func (n uniqueChildren) Create(context.Context, string, string, control.Settings) (control.Receipt, error) {
	*n.created++
	return control.Receipt{Thread: fmt.Sprintf("child-%d", *n.created), Session: fmt.Sprintf("child-session-%d", *n.created), Path: "/child-rollout"}, nil
}

func TestSpawnCountsAcceptedNonemptyPreloads(t *testing.T) {
	retained, err := history.NewService(control.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := retained.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "rollout.jsonl")
	cwd, _ := json.Marshal(workspace)
	roleTestFile(t, path, fmt.Sprintf(`{"type":"session_meta","payload":{"id":"root"}}
{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"root","thread_settings":{"model":"model","model_provider_id":"provider","cwd":%s,"permission_profile":{"type":"disabled"},"approval_policy":"never"}}}
{"type":"turn_context","payload":{"turn_id":"root-turn","model":"model","cwd":%s,"permission_profile":{"type":"disabled"},"approval_policy":"never"}}
`, cwd, cwd))
	service := control.New()
	if err := service.AdmitRoot(t.Context(), session, &control.Member{Thread: "root", Session: "session", Task: "/root", Path: path, AudienceAdmitted: true, Settings: control.Settings{Workspace: workspace, Model: "model"}}); err != nil {
		t.Fatal(err)
	}
	driver, err := NewDriver(service)
	if err != nil {
		t.Fatal(err)
	}
	host := &reconciliationHost{}
	native := uniqueChildren{instructionNative: instructionNative{Native: host, owner: instructionRuntime{info: control.ThreadInfo{Thread: "root", Session: "session", Path: path}}}, created: new(0)}
	if err := driver.Connect(native, host); err != nil {
		t.Fatal(err)
	}
	driver.models = []Model{{Name: "model", Root: new("ROOT"), Child: new("CHILD")}}
	admitted := control.Identity{Thread: "root", Session: "session", Turn: "root-turn", Call: "admit", Item: "exec"}
	if err := driver.AdmitRequest(t.Context(), session, admitted, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		call, preload string
		want          uint64
	}{
		{"with-preload", `,"preload":{"sources":[{"path":"task.md"}]}`, 1},
		{"with-preload", `,"preload":{"sources":[{"path":"task.md"}]}`, 1}, // Repeated call reuses its spawn.
		{"empty-preload", `,"preload":{"sources":[]}`, 1},
		{"without-preload", ``, 1},
	} {
		caller := admitted
		caller.Call = tc.call
		args := jsontext.Value(fmt.Sprintf(`{"task_name":%q,"message":"assignment","fork_turns":"none"%s}`, tc.call, tc.preload))
		if *native.created == 0 {
			// A lost assignment acknowledgement is reconciled by the repeated call.
			host.failure = &binding.DispatchError{Outcome: binding.Uncertain, Cause: errors.New("lost assignment acknowledgement")}
			if _, err := driver.Call(t.Context(), caller, "spawn_agent", args); err == nil {
				t.Fatal("spawn with lost acknowledgement succeeded")
			}
			if self, _, _ := service.FeatureUse("root"); self.SpawnPreloads != 0 {
				t.Fatalf("unconfirmed spawn counted %d preloads", self.SpawnPreloads)
			}
			host.failure = nil
		}
		if _, err := driver.Call(t.Context(), caller, "spawn_agent", args); err != nil {
			t.Fatalf("%s: %v", tc.call, err)
		}
		if self, _, _ := service.FeatureUse("root"); self.SpawnPreloads != tc.want {
			t.Fatalf("%s: spawn preloads = %d, want %d", tc.call, self.SpawnPreloads, tc.want)
		}
	}
	for _, options := range []string{`"fork_turns":"all","preload":{"sources":[{"path":"task.md"}]}`, `"fork_turns":"none","preload":{"sources":[{"file":"task.md"}]}`} {
		caller := admitted
		caller.Call = "rejected"
		if _, err := driver.Call(t.Context(), caller, "spawn_agent", jsontext.Value(`{"task_name":"rejected","message":"assignment",`+options+`}`)); err == nil {
			t.Fatalf("spawn accepted invalid preload %s", options)
		}
	}
	if self, _, _ := service.FeatureUse("root"); self.Spawns != 3 || self.PreloadRejections != 2 {
		t.Fatalf("spawns = %d, preload rejections = %d, want 3 and 2", self.Spawns, self.PreloadRejections)
	}
}
