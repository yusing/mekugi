package requestexecution

import (
	"context"
	"net/http"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type identityPreparer struct{ input Preparation }

func (p *identityPreparer) Prepare(_ context.Context, _ *codexresponses.Request, input Preparation) (Prepared, error) {
	p.input = input
	return nil, nil
}

func TestPreparedChildObservationIdentity(t *testing.T) {
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
	limits := control.Limits{Concurrency: 4, Depth: 1}
	check(service.AdmitRoot(t.Context(), session, &control.Member{Thread: "root", Session: "root-session", Task: "/root", Turn: "root-turn", Generation: 1, Status: control.Working, HostRunning: true, AudienceAdmitted: true, Limits: limits}))
	caller := control.Identity{Thread: "root", Session: "root-session", Turn: "root-turn", Call: "spawn", Item: "exec"}
	spawn, err := service.ReserveSpawn(t.Context(), caller, control.SpawnRequest{Name: "worker", Message: "Work.", Settings: control.Settings{Workspace: t.TempDir()}, Limits: limits})
	check(err)
	for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
		_, err := service.ClaimEffect(t.Context(), "root", spawn.ID)
		check(err)
		check(service.EffectAcknowledged(t.Context(), "root", spawn.ID, control.Receipt{Phase: phase, Thread: "child", Session: "child-session", Turn: "child-turn"}))
	}
	for _, tc := range []struct{ thread, name, parent, kind string }{
		{"child", "/root/worker", "root", "thread_spawn"},
		{"root", "/root", "", ""},
	} {
		headers := make(http.Header)
		headers.Set(codexresponses.ThreadIDHeader, tc.thread)
		const nativeMetadata = `{"request_kind":"turn","turn_id":"host-turn","agent_name":"/root"}`
		headers.Set(codexresponses.TurnMetadataHeader, nativeMetadata)
		request, err := codexresponses.ParseRequest([]byte(`{"model":"gpt-test","input":[],"tools":[]}`))
		check(err)
		observer := new(identityPreparer)
		attempt := newRequestAttempt(Executor{ThreadControl: service, Observations: observer}, t.Context(), t.Context(), request, headers, tc.thread+"-session")
		check(attempt.prepare())
		metadata := observer.input.Metadata
		if metadata.AgentName != tc.name || metadata.ParentThreadID != tc.parent || metadata.SubagentKind != tc.kind {
			t.Fatalf("observation lost admitted identity: %+v", metadata)
		}
		if headers.Get(codexresponses.TurnMetadataHeader) != nativeMetadata {
			t.Fatal("logical identity rewrote host wire metadata")
		}
	}
}
