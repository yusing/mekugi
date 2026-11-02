package executionobservation

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type completionTransport struct{ params jsontext.Value }

func (*completionTransport) Initialize() (string, error)       { return "id", nil }
func (*completionTransport) Respond(jsontext.Value, any) error { return nil }
func (wire *completionTransport) Send(_ string, params any, _ bool) (string, error) {
	wire.params, _ = json.Marshal(params)
	return "id", nil
}

type completionReads struct {
	changes.Service
	reads atomic.Int32
}

func (reader *completionReads) Read(ctx context.Context, selected changes.Scope, query changes.Query) (string, error) {
	reader.reads.Add(1)
	return reader.Service.Read(ctx, selected, query)
}

func TestCompletionInjectionCarriesPreparedEvidenceWithoutAnotherRead(t *testing.T) {
	f := newMChangesSliceFixture(t, "completion")
	id := f.reserve(t, f.thread, "edit")
	f.publish(t, id, "edit", "edit-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file.txt", "file.txt", "before\n", "after\n")}})
	p := newManagedMekugiProxy(t)
	setTestProxyStore(p, f.store)
	reader := &completionReads{Service: p.Changes}
	p.Changes = reader
	retained, err := history.NewService(control.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := retained.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, f.thread)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	service := control.New()
	if err := service.AdmitRoot(t.Context(), session, &control.Member{Thread: f.thread, Session: "runtime", Task: "/root", AudienceAdmitted: true, Settings: control.Settings{Workspace: f.workspace}}); err != nil {
		t.Fatal(err)
	}
	p.ThreadControl, err = threadbinding.NewDriver(service)
	if err != nil {
		t.Fatal(err)
	}
	selection := string(mustTestJSON(t, map[string]any{"sources": []any{map[string]any{"changes": []string{id}}}}))
	input := control.NativeInput{Thread: f.thread, ID: "final", Kind: "FINAL_ANSWER", Outcome: control.Completed, Sender: "/root/worker", Recipient: "/root", Message: "Finished.\n" + compact.CompactMetadataStart + selection + compact.CompactMetadataEnd}
	wire := new(completionTransport)
	requests := sessioncontrol.NewRequests[struct{}](wire)
	requests.PrepareCompletion = p.PrepareCompletion
	done := make(chan error, 1)
	go func() { _, err := requests.Input(t.Context(), input); done <- err }()
	requests.Dispatch(<-requests.Calls())
	var sent struct {
		Items []map[string]jsontext.Value `json:"items"`
	}
	if err := json.Unmarshal(wire.params, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Items) != 3 {
		t.Fatalf("injection did not carry answer, reader and result: %s", wire.params)
	}
	if !requests.CompleteCall(appserver.Message{ID: []byte("id"), Result: []byte(`{}`)}) {
		t.Fatal("injection was not acknowledged")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": sent.Items, "tools": testExecResponsesTools()}))
		if err != nil {
			t.Fatal(err)
		}
		meta := codexresponses.TurnMetadata{ThreadID: f.thread, TurnID: "parent-turn", RequestKind: "turn"}
		pending, _, _, err := p.preparePreloadInput(&request, "runtime", f.thread, meta, false)
		if err != nil || len(pending) != 0 {
			t.Fatalf("injected completion scheduled acquisition: %s, %v", pending, err)
		}
		// Execution-free preparation hydrates the same receiver context without
		// engaging this fixture's unrelated host-instruction acquisition.
		request.Fields["tools"] = []byte(`[]`)
		if _, err := p.prepareModelRequest(t.Context(), &request, "runtime", f.thread, meta, true, false); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(request.Fields["input"], []byte("+after")) || bytes.Contains(request.Fields["input"], []byte(compact.SnapshotPrefix)) {
			t.Fatalf("receiver lost prepared evidence: %s", request.Fields["input"])
		}
	}
	if reader.reads.Load() != 1 {
		t.Fatalf("completion acquired evidence %d times", reader.reads.Load())
	}
	failed, err := new(Preparer).PrepareCompletion(t.Context(), input)
	if err != nil || len(failed.Items) != 2 || !strings.Contains(string(failed.Items[1]), "CONTENT NOT ATTACHED") {
		t.Fatalf("attachment failure lost the final delivery: %+v, %v", failed, err)
	}
	input.Outcome = control.Failed
	if prepared, err := p.PrepareCompletion(t.Context(), input); err != nil || len(prepared.Items) != 0 || reader.reads.Load() != 1 {
		t.Fatal("failed work acquired a successful attachment", err)
	}
}
