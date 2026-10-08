package router

import (
	"bytes"
	"encoding/json/jsontext"
	"reflect"
	"strings"
	"testing"
	"time"
)

func resetPlan(t *testing.T, proxy *mekugiProxy, workspace, thread string) {
	t.Helper()
	_, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{
		Op: "plan", Reset: "slice", Tasks: []jsontext.Value{
			jsontext.Value(`{"title":"First","state":"working"}`),
			jsontext.Value(`"Second"`),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJournalResetTurnBoundaryAndDurability(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	resetPlan(t, proxy, workspace, thread)
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, thread, "turn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
		t.Fatal(err)
	}
	intent, err := proxy.journals.completedSlice(t.Context(), proxy.replayStore, workspace, thread, "turn-1")
	if err != nil || intent == nil || intent.Path != "/2" || intent.Phase != "pending" || intent.Turn != "turn-1" {
		t.Fatalf("slice intent=%+v err=%v", intent, err)
	}
	if duplicate, err := proxy.journals.completedSlice(t.Context(), proxy.replayStore, workspace, thread, "turn-1"); err != nil || duplicate != nil {
		t.Fatalf("duplicate intent=%+v err=%v", duplicate, err)
	}
	reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.resetIntent(t.Context(), workspace, thread)
	if err != nil || persisted == nil || persisted.ID != intent.ID {
		t.Fatalf("replayed intent=%+v err=%v", persisted, err)
	}
	if got, err := reopened.resetIntent(t.Context(), t.TempDir(), thread); err != nil || got != nil {
		t.Fatalf("cross-workspace intent=%+v err=%v", got, err)
	}
	if got, err := reopened.resetIntent(t.Context(), workspace, "other"); err != nil || got != nil {
		t.Fatalf("cross-thread intent=%+v err=%v", got, err)
	}
	if err := proxy.journals.changeReset(t.Context(), reopened, workspace, thread, intent.ID, func(j *threadJournal, _ *journalResetIntent) error { j.ResetIntent = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := proxy.journals.completedSlice(t.Context(), reopened, workspace, thread, "turn-1"); err != nil || duplicate != nil {
		t.Fatalf("cancelled turn rearmed=%+v err=%v", duplicate, err)
	}
	if err := proxy.journals.beginJournalTurn(t.Context(), reopened, workspace, thread, "turn-2"); err != nil {
		t.Fatal(err)
	}
	if next, err := proxy.journals.completedSlice(t.Context(), reopened, workspace, thread, "turn-2"); err != nil || next == nil || !next.Resume {
		t.Fatalf("unfinished work should continue without reusing slice boundary: %+v err=%v", next, err)
	}
}

func TestJournalResetRequiresNewDoneTransitionAndPendingSibling(t *testing.T) {
	for _, scenario := range []string{"working", "blocked", "dropped", "removed", "no-pending"} {
		t.Run(scenario, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			resetPlan(t, proxy, workspace, thread)
			if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, thread, "turn"); err != nil {
				t.Fatal(err)
			}
			mutations := []journalMutation{}
			switch scenario {
			case "blocked":
				mutations = append(mutations, journalMutation{Op: "set", P: "/1", State: new("blocked"), Reason: new("waiting")})
			case "dropped":
				mutations = append(mutations, journalMutation{Op: "set", P: "/1", State: new("dropped"), Reason: new("obsolete")})
			case "removed":
				mutations = append(mutations, journalMutation{Op: "remove", P: "/1"})
			case "no-pending":
				mutations = append(mutations, journalMutation{Op: "set", P: "/2", State: new("working")}, journalMutation{Op: "set", P: "/1", State: new("done")})
			}
			if len(mutations) > 0 {
				if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", mutations); err != nil {
					t.Fatal(err)
				}
			}
			intent, err := proxy.journals.completedSlice(t.Context(), proxy.replayStore, workspace, thread, "turn")
			if err != nil || (scenario == "blocked" && intent != nil) || (scenario != "blocked" && (intent == nil || !intent.Resume)) {
				t.Fatalf("scenario %s generated intent=%+v err=%v", scenario, intent, err)
			}
		})
	}
}

func TestJournalSliceAncestorEligibility(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"dropped", "delegated", "non-leaf"} {
		t.Run(scenario, func(t *testing.T) {
			d, wire := resetDriverPlanFixture(t, "off", "/1")
			mutations := []journalMutation{{Op: "add", Kind: "task", Title: new("Independent work"), State: new("working")}}
			switch scenario {
			case "dropped":
				mutations = append(mutations, journalMutation{Op: "set", P: "/1", State: new("dropped"), Reason: new("Abandoned plan")})
			case "delegated":
				mutations = append(mutations, journalMutation{Op: "set", P: "/1", Agent: "/root/worker"})
			case "non-leaf":
				mutations = append(mutations, journalMutation{Op: "add", Under: "/1/2", Kind: "task", Title: new("Subtask")})
			}
			if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", mutations); err != nil {
				t.Fatal(err)
			}
			before, _, err := readThreadJournal(d.proxy.replayStore, d.workspace, d.thread)
			if err != nil {
				t.Fatal(err)
			}
			next := before.continuationCandidate("first-turn")
			want := "/2"
			if scenario == "non-leaf" {
				want = "/1/2"
			}
			if next == nil || next.Path != want || next.Resume != (scenario != "non-leaf") || !before.continuationCurrent(next) {
				t.Fatalf("candidate=%+v want current target %s", next, want)
			}
			_, err = d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "finish-check", []journalMutation{
				{Op: "log", Text: new("Final report")}, {Op: "finish", finishTurn: "first-turn"},
			})
			if scenario == "non-leaf" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "runnable task /2") {
					t.Fatalf("premature finish error=%v", err)
				}
				after, _, readErr := readThreadJournal(d.proxy.replayStore, d.workspace, d.thread)
				if readErr != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("rejected finish changed journal: %v", readErr)
				}
			}
			if err := d.tick(time.Unix(102, 0)); err != nil {
				t.Fatal(err)
			}
			if scenario == "non-leaf" {
				resetDriverRequireMethods(t, wire, "turn/start")
			} else {
				resetDriverRequireMethods(t, wire)
			}
		})
	}
}

func TestJournalResetForkDoesNotInheritLiveIntent(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	resetPlan(t, proxy, workspace, thread)
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, thread, "turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
		t.Fatal(err)
	}
	if intent, err := proxy.journals.completedSlice(t.Context(), proxy.replayStore, workspace, thread, "turn"); err != nil || intent == nil {
		t.Fatalf("source intent=%+v err=%v", intent, err)
	}
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "fork", "/root", thread); err != nil {
		t.Fatal(err)
	}
	fork, exists, err := readThreadJournal(proxy.replayStore, workspace, "fork")
	if err != nil || !exists {
		t.Fatalf("fork read exists=%v err=%v", exists, err)
	}
	if fork.ResetIntent != nil || fork.TurnID != "" || fork.ResetHandledTurn != "" {
		t.Fatalf("fork inherited live reset state: %+v", fork)
	}
}

func TestJournalSliceCompactionConsumesOnlyArmedManualIntent(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	proxy.journalCompaction = "slice"
	resetPlan(t, proxy, workspace, thread)
	if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, thread, "slice-turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
		t.Fatal(err)
	}
	intent, err := proxy.journals.completedSlice(t.Context(), proxy.replayStore, workspace, thread, "slice-turn")
	if err != nil || intent == nil {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
	request, headers := journalCompactionRequest(t, workspace, thread)
	metadata, ok := decodeCodexTurnMetadata(headers)
	if !ok {
		t.Fatal("compaction metadata")
	}
	metadata.Compaction = mustTestJSON(t, map[string]any{"trigger": "manual", "phase": "standalone_turn", "reason": "user_requested", "implementation": "responses", "strategy": "memento"})
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
	providerWire, err := journalCompactionSSE("upstream", "gpt-test", "Provider summary")
	if err != nil {
		t.Fatal(err)
	}
	providerResponse := serverHTTPResponse(string(providerWire))
	providerResponse.Header.Set("Content-Type", "text/event-stream")
	provider := &serverFakeProvider{}
	forward := func() string {
		t.Helper()
		providerResponse = serverHTTPResponse(string(providerWire))
		providerResponse.Header.Set("Content-Type", "text/event-stream")
		provider.results = append(provider.results, serverForwardResult{response: providerResponse})
		var output bytes.Buffer
		if err := executeRequest(t.Context(), t.Context(), request, headers, "compact", provider, &output, nil, proxy); err != nil {
			t.Fatal(err)
		}
		return output.String()
	}
	if output := forward(); !strings.Contains(output, "Provider summary") || len(provider.forwarded) != 1 {
		t.Fatalf("unarmed slice did not forward: %q forwards=%d", output, len(provider.forwarded))
	}
	if err := proxy.journals.changeReset(t.Context(), proxy.replayStore, workspace, thread, intent.ID, func(_ *threadJournal, i *journalResetIntent) error { i.Phase = "armed"; return nil }); err != nil {
		t.Fatal(err)
	}
	metadata.Compaction = mustTestJSON(t, map[string]any{"trigger": "auto", "phase": "standalone_turn", "reason": "context_limit", "implementation": "responses", "strategy": "memento"})
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
	if output := forward(); !strings.Contains(output, "Provider summary") || len(provider.forwarded) != 2 {
		t.Fatalf("automatic slice compaction consumed intent: %q forwards=%d", output, len(provider.forwarded))
	}
	metadata.Compaction = mustTestJSON(t, map[string]any{"trigger": "manual", "phase": "standalone_turn", "reason": "user_requested", "implementation": "responses", "strategy": "memento"})
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "armed-compact", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 2 || !strings.Contains(output.String(), "Second") {
		t.Fatalf("armed slice not router-answered: %q forwards=%d", &output, len(provider.forwarded))
	}
	consumed, err := proxy.replayStore.resetIntent(t.Context(), workspace, thread)
	if err != nil || consumed == nil || consumed.Phase != "consumed" || consumed.ResponseID == "" {
		t.Fatalf("consumed intent=%+v err=%v", consumed, err)
	}
}
