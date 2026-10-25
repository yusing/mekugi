package codexbinding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestAdmitRequestCodexV2Limits(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		min, max     int64
	}{
		{"defaults", `{}`, 10_000, 3_600_000},
		{"legacy depth ignored", `{"agents":{"max_depth":0,"max_concurrent_threads_per_session":3}}`, 10_000, 3_600_000},
		{"V2 precedence and wait bounds", `{"agents":{"max_depth":1,"max_concurrent_threads_per_session":8},"features":{"multi_agent_v2":{"max_concurrent_threads_per_session":3,"min_wait_timeout_ms":0,"max_wait_timeout_ms":1500000}}}`, 0, 1_500_000},
		{"host sole-slot setting does not cap Mekugi", `{"features":{"multi_agent_v2":{"max_concurrent_threads_per_session":1}}}`, 10_000, 3_600_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			defer session.Close()
			workspace := t.TempDir()
			path := filepath.Join(workspace, "rollout.jsonl")
			cwd, err := json.Marshal(workspace)
			if err != nil {
				t.Fatal(err)
			}
			roleTestFile(t, path, fmt.Sprintf(`{"type":"session_meta","payload":{"id":"root"}}
{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"root","thread_settings":{"model":"parent","model_provider_id":"provider","cwd":%s,"permission_profile":{"type":"disabled"},"approval_policy":"never"}}}
{"type":"turn_context","payload":{"turn_id":"root-turn","model":"parent","cwd":%s,"permission_profile":{"type":"disabled"},"approval_policy":"never"}}
`, cwd, cwd))
			service := control.New()
			driver, err := NewDriver(service)
			if err != nil {
				t.Fatal(err)
			}
			runtime := instructionRuntime{info: control.ThreadInfo{Thread: "root", Session: "root-session", Path: path}, config: jsontext.Value(`{"layers":[],"config":` + tc.config + `}`)}
			host := &reconciliationHost{}
			if err := driver.Connect(instructionNative{Native: host, owner: runtime}, host); err != nil {
				t.Fatal(err)
			}
			caller := control.Identity{Thread: "root", Session: "root-session", Turn: "root-turn", Call: "spawn", Item: "exec"}
			if err := driver.AdmitRequest(t.Context(), session, caller, true); err != nil {
				t.Fatal(err)
			}
			member, _ := service.Member("root")
			limits := member.Limits
			if limits.MinWaitMS != tc.min || limits.MaxWaitMS != tc.max {
				t.Fatalf("captured limits = %+v", limits)
			}
			// Each next parent is a real admitted child from the preceding spawn.
			// Logical depth and host thread-count hints impose no Mekugi cap.
			depth := 1
			if tc.name == "defaults" {
				depth = 8
			}
			for slot := 1; slot <= depth; slot++ {
				id := fmt.Sprintf("child%d", slot)
				spawn, err := service.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: id, Message: "nested work", Settings: member.Settings, Limits: limits})
				if err != nil {
					t.Fatalf("nested spawn %d: %v", slot, err)
				}
				for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
					claimed, err := service.ClaimEffect(t.Context(), caller.Thread, spawn.ID)
					if err != nil || claimed == nil || claimed.Phase != phase {
						t.Fatalf("claim %s: %+v, %v", phase, claimed, err)
					}
					if err := service.EffectAcknowledged(t.Context(), caller.Thread, spawn.ID, control.Receipt{Phase: phase, Thread: id, Session: id + "-session", Turn: id + "-turn"}); err != nil {
						t.Fatal(err)
					}
				}
				caller = control.Identity{Thread: id, Session: id + "-session", Turn: id + "-turn", Call: "spawn", Item: "exec"}
			}
			_, err = service.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: "reviewer", Message: "independent review", Settings: member.Settings, Limits: limits})
			if err != nil {
				t.Fatalf("nested review rejected by a Mekugi cap: %v", err)
			}
		})
	}
}
