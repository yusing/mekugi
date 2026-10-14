package router

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestOrchestrateListDetails(t *testing.T) {
	text := strings.Repeat("retained evidence ", 2000)
	b := orchestrate.Batch{TaskName: "batch", State: "prepared", Error: "host unavailable",
		Launch: &orchestrate.Launch{Message: text, Params: jsontext.Value(`{"model":"model"}`)},
		Workflow: &orchestrate.Workflow{
			Assignment: orchestrate.WorkflowAssignment{Kind: orchestrate.WorkflowImplementation, Scope: text,
				RequiredChecks: []orchestrate.RequiredCheck{{ID: "check", Command: "make test"}}},
			Phase: orchestrate.PhaseNeedsDecision, Generation: 2, Candidate: &orchestrate.Candidate{Revision: strings.Repeat("a", 40)},
			Reason: "review dispatch unconfirmed", Report: text, ReviewWaiver: "User requested correction-only delivery",
			Checks: []orchestrate.CheckResult{{Generation: 2, Revision: strings.Repeat("a", 40), CheckID: "check", Outcome: orchestrate.CheckUncertain, Output: text}},
			Reviews: []orchestrate.ReviewResult{{Revision: strings.Repeat("a", 40), Purpose: orchestrate.ReviewCorrectness,
				Verdict: orchestrate.VerdictFindings, Findings: []orchestrate.ReviewFinding{{ID: "finding", Detail: text}}}},
			PreviousReviews: []orchestrate.PreviousReview{{DispatchID: "settled", Result: orchestrate.ReviewResult{Revision: strings.Repeat("b", 40), Purpose: orchestrate.ReviewSimplification, Verdict: orchestrate.VerdictClear}}},
			Dispatches: []orchestrate.WorkflowDispatch{
				{ID: "settled", Generation: 1, State: orchestrate.DispatchAcknowledged, HostStatus: "completed"},
				{ID: "old-intent", Generation: 1, State: orchestrate.DispatchIntent, Params: jsontext.Value(`{"developerInstructions":"retained evidence"}`)},
				{ID: "old-uncertain", Generation: 1, State: orchestrate.DispatchUncertain, Reason: "inspect old host effect"},
				{ID: "old-active", Generation: 1, State: orchestrate.DispatchAcknowledged},
				{ID: "current", Generation: 2, State: orchestrate.DispatchUncertain, Reason: "inspect host"},
			},
			Outcome: &orchestrate.WorkflowOutcome{State: orchestrate.DispatchUncertain, Message: text},
		},
		Evidence:    []orchestrate.Evidence{{Name: "input", State: "failed", Error: "copy failed"}},
		ShadowMerge: &orchestrate.ShadowMerge{State: "writing"},
	}
	before := mustMarshalJSON(&b)
	summaryDispatches := slices.Clone(b.Workflow.Dispatches[1:])
	for i := range summaryDispatches {
		summaryDispatches[i].Params = nil
	}
	for _, detail := range []string{"", "summary", "checks", "reviews", "full"} {
		t.Run(detail, func(t *testing.T) {
			agents, err := orchestrateListAgents([]orchestrate.Batch{b}, orchestrateListInput{Detail: detail})
			if err != nil || len(agents) != 1 {
				t.Fatal(agents, err)
			}
			if detail == "full" {
				if !reflect.DeepEqual(agents[0], b) {
					t.Fatal("full detail lost retained evidence")
				}
				return
			}
			a := agents[0].(map[string]any)
			w := a["workflow"].(map[string]any)
			if a["state"] != b.State || a["error"] != b.Error || w["phase"] != b.Workflow.Phase || w["reason"] != b.Workflow.Reason || w["review_waiver"] != b.Workflow.ReviewWaiver ||
				!reflect.DeepEqual(w["candidate"], b.Workflow.Candidate) || w["outcome_state"] != orchestrate.DispatchUncertain ||
				!reflect.DeepEqual(w["dispatches"], summaryDispatches) || a["evidence"] == nil || a["shadow_merge"] == nil {
				t.Fatal("summary lost retained state or blockers", a)
			}
			if !reflect.DeepEqual(a["launch"], map[string]any{"thread_id": "", "turn_id": "", "host_status": ""}) {
				t.Fatal("unconfirmed launch lost or settings exposed")
			}
			switch detail {
			case "checks":
				if !reflect.DeepEqual(w["checks"], b.Workflow.Checks) || !reflect.DeepEqual(w["required_checks"], b.Workflow.Assignment.RequiredChecks) || w["reviews"] != nil {
					t.Fatal("check selection lost or mixed evidence")
				}
			case "reviews":
				if !reflect.DeepEqual(w["reviews"], b.Workflow.Reviews) || !reflect.DeepEqual(w["previous_reviews"], b.Workflow.PreviousReviews) || w["checks"] != nil {
					t.Fatal("review selection lost or mixed evidence")
				}
			default:
				if len(mustMarshalJSON(agents)) > 1500 || bytes.Contains(mustMarshalJSON(agents), []byte("retained evidence")) {
					t.Fatal("summary includes verbose evidence")
				}
			}
			if w["assignment"] != nil || w["report"] != nil || w["outcome"] != nil {
				t.Fatal("summary exposes prompts or reports")
			}
		})
	}
	if !bytes.Equal(before, mustMarshalJSON(&b)) {
		t.Fatal("inspection changed retained evidence")
	}
}

func TestOrchestrateListRetainedStates(t *testing.T) {
	empty, err := orchestrateListAgents(nil, orchestrateListInput{})
	if err != nil || string(mustMarshalJSON(empty)) != "[]" {
		t.Fatal("empty run must return an empty list", empty, err)
	}
	batches := []orchestrate.Batch{{TaskName: "prepared", State: "prepared"}, {TaskName: "removed", State: "removed"}, {TaskName: "failed", State: "failed", Error: "preparation failed"},
		{TaskName: "ready", State: "prepared", Launch: &orchestrate.Launch{ThreadID: "thread", TurnID: "turn", HostStatus: "completed"},
			Workflow: &orchestrate.Workflow{Phase: orchestrate.PhaseReady, Candidate: &orchestrate.Candidate{Revision: "candidate"}}}}
	agents, err := orchestrateListAgents(batches, orchestrateListInput{})
	var got []orchestrate.Batch
	if err != nil || json.Unmarshal(mustMarshalJSON(agents), &got) != nil || len(got) != len(batches) {
		t.Fatal("retained batches lost", agents, err)
	}
	for i, b := range batches {
		if got[i].TaskName != b.TaskName || got[i].State != b.State || got[i].Error != b.Error {
			t.Fatal("retained state changed", got)
		}
	}
	if got[0].Workflow != nil || got[0].Launch != nil || got[3].Workflow.Phase != orchestrate.PhaseReady || got[3].Launch.HostStatus != "completed" {
		t.Fatal("unknown state or readiness changed", got)
	}
	selected, err := orchestrateListAgents(batches, orchestrateListInput{Targets: jsontext.Value(`["/root/ready","prepared","ready"]`), Detail: "full"})
	if err != nil || !reflect.DeepEqual(selected, []any{batches[0], batches[3]}) {
		t.Fatal("selection must deduplicate and preserve run order", selected, err)
	}
}
