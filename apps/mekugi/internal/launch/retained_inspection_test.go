package launch

import (
	json "encoding/json/v2"
	"fmt"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestThreadInspectionWithoutRuntime(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	for _, tc := range []struct {
		name, reply, want string
		rejected          bool
	}{
		{name: "retained", reply: `{"agent":{"threadId":"retained","rootThreadId":"main","loaded":false,"model":"retained","reasoningEffort":"high","serviceTier":"priority","metrics":{"contextTokens":123,"contextWindow":1000,"completedCompactions":1}}}`, want: `{"model":"retained","reasoning_effort":"high","service_tier":"priority","context_tokens":123,"context_window":1000,"compactions":1}`},
		{name: "unavailable", reply: `{"agent":{"threadId":"retained","rootThreadId":"main","loaded":false,"metrics":{}}}`, want: `{"model":null,"reasoning_effort":null,"service_tier":null,"context_tokens":null,"context_window":null,"compactions":null}`},
		{name: "foreign", rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			var got []byte
			var err error
			go func() {
				defer close(done)
				details, failure := u.Requests.ObserveAgent(t.Context(), tc.name)
				err = failure
				if err == nil {
					got, err = json.Marshal(&details)
				}
			}()
			u.Requests.DispatchMeasurement(<-u.Requests.Inspections())
			request := appServerOneRequest(t, u.Host.Input.(*appServerTestInput), "agent/observe", "")
			response := appserver.Message{ID: []byte(fmt.Sprint(request.ID)), Result: []byte(tc.reply)}
			if tc.rejected {
				response.Error = &appserver.Error{Message: "not admitted"}
			}
			if !u.Requests.CompleteCall(response) {
				t.Fatal("passive observation response missing")
			}
			<-done
			if tc.rejected {
				if err == nil {
					t.Fatal("native rejection hidden")
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("passive inspection: %s %v", got, err)
			}
			if calls := appServerTurnRequests(t, u.Host.Input.(*appServerTestInput)); len(calls) != 0 {
				t.Fatalf("inspection acquired runtime: %+v", calls)
			}
		})
	}
}
