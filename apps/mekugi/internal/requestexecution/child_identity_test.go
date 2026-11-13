package requestexecution

import (
	"context"
	"net/http"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
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
	for _, tc := range []struct{ thread, name, parent, kind, native string }{
		{"child", "/root/native-worker", "root", "thread_spawn", `{"request_kind":"turn","turn_id":"host-turn","thread_id":"child","agent_name":"/root/native-worker","parent_thread_id":"root","subagent_kind":"thread_spawn"}`},
		{"root", "/root", "", "", `{"request_kind":"turn","turn_id":"host-turn","agent_name":"/root"}`},
		{"child", "/root", "", "", `{"request_kind":"turn","turn_id":"host-turn","agent_name":"/root"}`},
	} {
		headers := make(http.Header)
		headers.Set(codexresponses.ThreadIDHeader, tc.thread)
		headers.Set(codexresponses.TurnMetadataHeader, tc.native)
		request, err := codexresponses.ParseRequest([]byte(`{"model":"gpt-test","input":[],"tools":[]}`))
		check(err)
		observer := new(identityPreparer)
		attempt := newRequestAttempt(Executor{Observations: observer}, t.Context(), t.Context(), request, headers, tc.thread+"-session")
		check(attempt.prepare())
		metadata := observer.input.Metadata
		if metadata.AgentName != tc.name || metadata.ParentThreadID != tc.parent || metadata.SubagentKind != tc.kind {
			t.Fatalf("observation replaced native identity with Go membership: %+v", metadata)
		}
		if headers.Get(codexresponses.TurnMetadataHeader) != tc.native {
			t.Fatal("logical identity rewrote host wire metadata")
		}
	}
}
