package executionobservation

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
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
			wake := control.NativeInput{ID: "root:wait", Wake: &control.WaitResult{ID: "wait", State: "timeout", Metrics: map[string]control.SessionMetrics{"/root/child": {ContextTokens: new(uint64(123)), Compactions: new(uint64(2))}}}}
			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
				"model": "fixture", "stream": true, "tool_choice": "auto", "parallel_tool_calls": false,
				"input": []any{
					map[string]any{"type": "message", "id": control.MessageItemID("root:message"), "role": "assistant", "content": "progress"},
					map[string]any{"type": "function_call_output", "namespace": "threadcontrol", "name": "wait_agent", "output": wake.RetainedWaitOutput()},
				},
			}))
			check(err)
			metadata, ok := codexresponses.DecodeTurnMetadata(serverCompactionMetadataHeaders(t))
			if !ok {
				t.Fatal("invalid fixture metadata")
			}
			metadata.RequestKind = kind
			_, err = proxy.Prepare(t.Context(), &request, requestexecution.Preparation{Session: "session", Thread: "root", Metadata: metadata, MetadataValid: true})
			check(err)
			wire, err := request.WireBody(request.Fields)
			check(err)
			var forwarded struct {
				Input []map[string]jsontext.Value `json:"input"`
			}
			check(json.Unmarshal(wire, &forwarded))
			if len(forwarded.Input) != 2 || responses.StringField(forwarded.Input[0], "id") != "" || responses.StringField(forwarded.Input[0], "content") != "progress" {
				t.Fatalf("invalid provider message: %s", wire)
			}
			if !bytes.Contains(request.OriginalField("input"), []byte(control.MessageItemID("root:message"))) {
				t.Fatal("retained host identity changed")
			}
			if !responses.SameJSONValue([]byte(responses.StringField(forwarded.Input[1], "output")), responses.MustMarshalJSON(wake.Wake)) || !bytes.Contains(request.OriginalField("input"), []byte(wake.ID)) {
				t.Fatal("wake projection changed metrics, exposed identity or rewrote retained input")
			}
			tree, err := service.Snapshot("root")
			check(err)
			if !tree.Effect("root", "message").Consumed || tree.Members["root"].HostRunning || tree.Members["root"].Status != control.Completed {
				t.Fatal("input read without execution did not consume the notification or started work")
			}
		})
	}
}

func TestApprovalReviewProjectsRetainedWakeIdentity(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	wake := control.NativeInput{ID: "root:wait", Wake: &control.WaitResult{State: "timeout"}}
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"model": "fixture", "input": []any{
		map[string]any{"type": "function_call_output", "namespace": "threadcontrol", "name": "wait_agent", "output": wake.RetainedWaitOutput()},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.Prepare(t.Context(), &request, requestexecution.Preparation{ApprovalReview: true}); err != nil {
		t.Fatal(err)
	}
	wire, err := request.WireBody(request.Fields)
	if err != nil || bytes.Contains(wire, []byte(wake.ID)) || !bytes.Contains(request.OriginalField("input"), []byte(wake.ID)) {
		t.Fatalf("review projection did not separate retained identity: %s, %v", wire, err)
	}
}
