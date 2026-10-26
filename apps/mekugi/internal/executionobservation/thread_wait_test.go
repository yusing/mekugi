package executionobservation

import (
	"encoding/json/jsontext"
	"testing"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestFailedWaitExecutionAllowsRetry(t *testing.T) {
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	retained, err := history.NewService(control.RecordKinds())
	check(err)
	store, err := retained.Open(t.Context(), t.TempDir())
	check(err)
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
	check(err)
	t.Cleanup(session.Close)
	service := control.New()
	check(service.AdmitRoot(t.Context(), session, &control.Member{Thread: "root", Session: "session", Task: "/root", AudienceAdmitted: true}))
	caller := control.Identity{Thread: "root", Session: "session", Turn: "turn", Call: "spawn", Item: "exec"}
	check(service.RequestStarted(t.Context(), caller, true))
	spawn, err := service.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: "child", Message: "work", Settings: control.Settings{Workspace: t.TempDir()}})
	check(err)
	for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
		_, err := service.ClaimEffect(t.Context(), "root", spawn.ID)
		check(err)
		check(service.EffectAcknowledged(t.Context(), "root", spawn.ID, control.Receipt{Phase: phase, Thread: "child", Session: "child-session", Turn: "child-turn"}))
	}
	driver, err := threadbinding.NewDriver(service)
	check(err)
	p := &Preparer{threadControl: driver}
	for _, state := range []string{"Script failed", "Script terminated", "Script completed"} {
		caller.Call, caller.Item = state, "item-"+state
		_, err := service.RegisterWait(t.Context(), caller, nil, true, nil, time.Now())
		check(err)
		request := codexresponses.Request{Fields: map[string]jsontext.Value{"input": mustTestJSON(t, []any{
			map[string]any{"type": "custom_tool_call", "call_id": caller.Call, "id": caller.Item, "name": "exec"},
			map[string]any{"type": "custom_tool_call_output", "call_id": caller.Call, "output": state + "\nWall time 0.1 seconds\nOutput:\n"},
		})}}
		item, err := p.observeThreadWait(t.Context(), "root", "turn", "exec", &request, nil)
		check(err)
		stop, err := service.ClaimLocalStop(t.Context(), "root", "turn", caller.Item)
		check(err)
		if state == "Script completed" {
			if stop == nil || item != caller.Item {
				t.Fatalf("retry did not arm its own wait: item=%q stop=%+v", item, stop)
			}
		} else {
			member, _ := service.Member("root")
			if stop != nil || item != "" || member.Wait.State != "canceled" {
				t.Fatalf("failed cell retained wait: item=%q member=%+v", item, member)
			}
		}
	}
}
