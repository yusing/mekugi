package executionobservation

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
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

func TestCompletionInjectionOmitsYieldedCaptures(t *testing.T) {
	for _, scenario := range []string{"confirmed", "late interval", "extended record", "bounded out", "uncertain", "failed", "reset", "native window", "cold resume", "unknown context", "reset before dispatch", "reset after injection", "window after injection"} {
		t.Run(scenario, func(t *testing.T) {
			f := newMChangesSliceFixture(t, "yield-completion")
			id := f.reserve(t, f.thread, "edit")
			file := diffmodel.RenderReviewFile("file.txt", "file.txt", "before\n", "yielded\n")
			files := []diffmodel.ReviewFile{file}
			if scenario == "extended record" {
				files = append(files, diffmodel.RenderReviewFile("extension.txt", "extension.txt", "before\n", "new-content\n"))
			}
			f.publish(t, id, "edit", "edit-call", mekugiHistory{Caller: "/root/worker", ReviewFiles: files})
			ids := []string{id}
			if scenario == "bounded out" {
				unseen := f.reserve(t, f.thread, "unseen")
				f.publish(t, unseen, "unseen", "unseen-call", mekugiHistory{Caller: "/root/worker", ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("unseen.txt", "unseen.txt", "before\n", "new-content\n")}})
				ids = append(ids, unseen)
			}
			record, found, err := f.store.read(f.workspace, "edit-call", false)
			if err != nil || !found {
				t.Fatalf("capture: %v, %v", found, err)
			}
			publication := changepresentation.Publication{Files: []changepresentation.File{{Chunks: []changepresentation.Chunk{{Workspace: f.workspace, CaptureOrder: record.CaptureOrder, Review: file, Origin: changepresentation.Origin{Change: id, Caller: "/root/worker"}}}}}}
			seen := make(map[string]int)
			// Omitted progress evidence must remain eligible for completion.
			// Launch tests exercise the combined-output recovery bound.
			progress := control.WaitProgress{More: scenario == "bounded out"}
			for capture := range publication.Progress(map[string]bool{"/root/worker": true}, nil) {
				seen[capture.Key] = capture.Chunks
				progress.Agents = append(progress.Agents, control.AgentProgress{Task: capture.Caller, Changes: capture.Change})
			}
			p := newManagedMekugiProxy(t)
			setTestProxyStore(p, f.store)
			p.Control = new(sessioncontrol.Observations)
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
			// The native dispatcher has claimed this yield. Retain its exact progress,
			// then acknowledge delivery before preparing the final through Input.
			wait := &control.Wait{Effect: control.Effect{ID: "yield", Caller: f.thread, Target: f.thread, Kind: "wake", State: "dispatching", Generation: 1}, Yielded: true}
			if err := service.AdmitRoot(t.Context(), session, &control.Member{Thread: f.thread, Session: "runtime", Task: "/root", Generation: 1, Status: control.Waiting, Wait: wait, AudienceAdmitted: true, Settings: control.Settings{Workspace: f.workspace}}); err != nil {
				t.Fatal(err)
			}
			contextID := p.Control.ContextIdentity(f.thread)
			if scenario == "unknown context" {
				contextID = ""
			}
			if err := service.RetainWaitProgress(t.Context(), f.thread, "yield", contextID, progress, seen); err != nil {
				t.Fatal(err)
			}
			receipt := control.Receipt{Turn: "continued"}
			if scenario == "uncertain" || scenario == "failed" {
				receipt.Error = "delivery failed"
				receipt.Uncertain = scenario == "uncertain"
			}
			if err := service.EffectAcknowledged(t.Context(), f.thread, "yield", receipt); err != nil {
				t.Fatal(err)
			}
			p.ThreadControl, err = threadbinding.NewDriver(service)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "reset":
				p.Control.RefreshContext(f.thread)
			case "native window":
				p.Control.ObserveCompactionContext(f.thread, "new-window")
			case "cold resume":
				p.Control = new(sessioncontrol.Observations)
			case "late interval":
				f.publish(t, id, "edit", "late-call", mekugiHistory{Caller: "/root/worker", ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("late.txt", "late.txt", "before\n", "new-content\n")}})
			}
			selection := string(mustTestJSON(t, map[string]any{"sources": []string{"mchanges:" + strings.Join(ids, ",")}}))
			input := control.NativeInput{Thread: f.thread, ID: "final", Kind: "FINAL_ANSWER", Outcome: control.Completed, Sender: "/root/worker", Recipient: "/root", Message: "Finished.\n" + compact.CompactMetadataStart + selection + compact.CompactMetadataEnd}
			wire := new(completionTransport)
			requests := sessioncontrol.NewRequests[struct{}](wire)
			requests.PrepareCompletion = p.PrepareCompletion
			done := make(chan error, 1)
			go func() { _, err := requests.Input(t.Context(), input); done <- err }()
			call := <-requests.Calls()
			if scenario == "reset before dispatch" {
				p.Control.RefreshContext(f.thread)
			}
			requests.Dispatch(call)
			var sent struct {
				Items []map[string]jsontext.Value `json:"items"`
			}
			if err := json.Unmarshal(wire.params, &sent); err != nil {
				t.Fatal(err)
			}
			requests.CompleteCall(appserver.Message{ID: []byte("id"), Result: []byte(`{}`)})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if scenario == "reset after injection" {
				p.Control.RefreshContext(f.thread)
			}
			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": sent.Items, "tools": []any{}}))
			if err != nil {
				t.Fatal(err)
			}
			meta := codexresponses.TurnMetadata{ThreadID: f.thread, TurnID: "continued", RequestKind: "turn"}
			if scenario == "window after injection" {
				meta.ContextWindowID = "replacement"
			}
			reads := reader.reads.Load()
			wantReads := int32(2)
			if scenario == "uncertain" || scenario == "failed" || scenario == "reset" || scenario == "native window" || scenario == "cold resume" || scenario == "unknown context" {
				wantReads = 1
			}
			if reads != wantReads {
				t.Fatalf("completion variant acquisition count: got %d, want %d", reads, wantReads)
			}
			if _, err := p.prepareModelRequest(t.Context(), &request, "runtime", f.thread, meta, true, false); err != nil {
				t.Fatal(err)
			}
			wantYielded := scenario == "uncertain" || scenario == "failed" || scenario == "reset" || scenario == "native window" || scenario == "cold resume" || scenario == "unknown context" || scenario == "reset before dispatch" || scenario == "reset after injection" || scenario == "window after injection"
			if bytes.Contains(request.Fields["input"], []byte("+yielded")) != wantYielded {
				t.Fatalf("receiver repeated yielded capture in final attachment: %s", request.Fields["input"])
			}
			if (scenario == "late interval" || scenario == "extended record" || scenario == "bounded out") && !bytes.Contains(request.Fields["input"], []byte("+new-content")) {
				t.Fatalf("new capture lost: %s", request.Fields["input"])
			}
			if reader.reads.Load() != reads {
				t.Fatal("receiver repeated acquisition instead of selecting retained evidence")
			}
			// Explicit preload/reload and mchanges reads retain the full selected patch.
			selected := p.Retained
			selected.Thread = f.thread
			selected.Namespace, err = selected.Store.NamespaceForThread(f.thread)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := compact.ReadSources(t.Context(), selection, f.workspace, nil, compact.ChangeReader{Service: p.Changes, Scope: selected})
			if err != nil || len(snapshot.Sources) == 0 || !strings.Contains(snapshot.Sources[0].Stdout, "+yielded") {
				t.Fatalf("explicit read lost yielded patch: %+v, %v", snapshot, err)
			}
		})
	}
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
	selection := string(mustTestJSON(t, map[string]any{"sources": []string{"mchanges:" + id}}))
	input := control.NativeInput{Thread: f.thread, ID: "final", Kind: "FINAL_ANSWER", Outcome: control.Completed, SourceGeneration: 1, ParentTurn: "child-turn", Sender: "/root/worker", Recipient: "/root", Message: "Finished.\n" + compact.CompactMetadataStart + selection + compact.CompactMetadataEnd}
	oldSelection := string(mustTestJSON(t, map[string]any{"sources": []compact.SourceIdentity{{Changes: []string{id}}}}))
	oldInput := input
	oldInput.Message = strings.Replace(input.Message, selection, oldSelection, 1)
	if prepared, err := p.PrepareCompletion(t.Context(), oldInput); err != nil || len(prepared.Items) != 0 || reader.reads.Load() != 0 {
		t.Fatalf("new object completion triggered acquisition: %+v %v", prepared, err)
	}
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
	caller := control.Identity{Thread: f.thread, Session: "runtime", Turn: "parent-turn", Call: "checkpoint", Item: "checkpoint-exec"}
	if err := service.RequestStarted(t.Context(), caller, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterCompaction(t.Context(), caller, "", nil, nil, time.Now(), true, ""); err != nil {
		t.Fatal(err)
	}
	items := []any{map[string]any{"type": "custom_tool_call", "call_id": caller.Call, "id": caller.Item, "name": "exec"},
		map[string]any{"type": "custom_tool_call_output", "call_id": caller.Call, "output": "Script completed\nWall time 0.1 seconds\nOutput:\n"}}
	for _, item := range sent.Items {
		items = append(items, item)
	}
	request := codexresponses.Request{Fields: map[string]jsontext.Value{"input": mustTestJSON(t, items), "tools": mustTestJSON(t, testExecResponsesTools())}}
	_, calls, _, err := p.preparePreloadInput(&request, "runtime", f.thread, codexresponses.TurnMetadata{ThreadID: f.thread, TurnID: caller.Turn, RequestKind: "turn"}, false)
	if err != nil || !calls[responses.StringField(sent.Items[1], "call_id")] {
		t.Fatalf("completion evidence was not admitted: %v, %v", calls, err)
	}
	if item, err := p.observeThreadWait(t.Context(), f.thread, caller.Turn, "exec", &request, nil, calls); err != nil || item != caller.Item {
		t.Fatalf("completion attachment superseded checkpoint: item=%q, %v", item, err)
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
