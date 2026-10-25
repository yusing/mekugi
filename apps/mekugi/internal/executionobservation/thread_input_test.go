package executionobservation

import (
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestThreadInputConsumedWithoutExecution(t *testing.T) {
	for _, kind := range []responses.RequestKind{responses.Compaction, responses.Turn} {
		t.Run(string(kind), func(t *testing.T) {
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
			defer session.Close()
			service := control.New()
			check(service.AdmitRoot(t.Context(), session, &control.Member{Thread: "root", Session: "session", Task: "/root", Turn: "work", Status: control.Working, HostRunning: true, AudienceAdmitted: true}))
			caller := control.Identity{Thread: "root", Session: "session", Turn: "work", Call: "message"}
			_, err = service.PrepareAction(t.Context(), caller, "/root", "message", "progress")
			check(err)
			_, err = service.ClaimEffect(t.Context(), "root", "message")
			check(err)
			check(service.EffectAcknowledged(t.Context(), "root", "message", control.Receipt{}))
			check(service.HostCompleted(t.Context(), "root", "work", control.Completed, ""))
			driver, err := threadbinding.NewDriver(service)
			check(err)
			proxy := newManagedMekugiProxy(t)
			proxy.threadControl = driver
			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
				"model": "fixture", "stream": true, "tool_choice": "auto", "parallel_tool_calls": false,
				"input": []any{map[string]any{"type": "message", "id": control.MessageItemID("root:message"), "role": "assistant", "content": "progress"}},
			}))
			check(err)
			metadata, ok := codexresponses.DecodeTurnMetadata(serverCompactionMetadataHeaders(t))
			if !ok {
				t.Fatal("invalid fixture metadata")
			}
			metadata.RequestKind = kind
			_, err = proxy.prepareModelRequest(t.Context(), &request, "session", "root", metadata, true, false)
			check(err)
			tree, err := service.Snapshot("root")
			check(err)
			if !tree.Effect("root", "message").Consumed || tree.Members["root"].HostRunning || tree.Members["root"].Status != control.Completed {
				t.Fatal("input read without execution did not consume the notification or started work")
			}
		})
	}
}
