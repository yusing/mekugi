package sessioncontrol

import (
	"errors"
	"testing"
)

func TestIdentifiedToolInputLifecycle(t *testing.T) {
	var o Observations
	var first, second Generation
	o.SetToolInput(&first, "", "main", GenerationItem{ID: "same"}, true)
	o.IdentifyToolInput(&first, GenerationItem{ID: "same"}, "exec", true, `await tools.exec_command({cmd: "long`)
	if got := o.ToolInput("main"); got != "Executing a command" || o.ToolInput("child") != "" {
		t.Fatalf("identified tool routing: %q", got)
	}
	o.SetToolInput(&second, "", "main", GenerationItem{ID: "same"}, true)
	o.IdentifyToolInput(&second, GenerationItem{ID: "same"}, "mcp__threadcontrol__spawn_agent", false, `{"task_name":"worker", "message":"long`)
	if got := o.ToolInput("main"); got != "Spawning subagent `worker`" {
		t.Fatal(got)
	}
	o.EndGeneration(&first)
	if got := o.ToolInput("main"); got != "Spawning subagent `worker`" {
		t.Fatalf("older request cleared newer generation: %q", got)
	}
	o.SetToolInput(&second, "", "main", GenerationItem{ID: "same"}, false)
	if o.ToolInput("main") != "" || o.EmittingToolInput("", "main") {
		t.Fatal("finished input survived its existing lifecycle")
	}
	o.IdentifyToolInput(&second, GenerationItem{ID: "same"}, "exec_command", false, "late")
	if o.ToolInput("main") != "" {
		t.Fatal("late input revived a completed item")
	}
}

func TestIdentifiedToolInputCorrelatesIndexAndID(t *testing.T) {
	var observations Observations
	var generation Generation
	first := GenerationItem{ID: "call", Index: 0, Indexed: true}
	second := GenerationItem{ID: "call", Index: 1, Indexed: true}
	observations.SetToolInput(&generation, "", "main", first, true)
	observations.IdentifyToolInput(&generation, GenerationItem{Index: 0, Indexed: true}, "apply_patch", false, "")
	observations.SetToolInput(&generation, "", "main", second, true)
	observations.IdentifyToolInput(&generation, second, "exec_command", false, "")
	if got := observations.ToolInput("main"); got != "Executing a command" {
		t.Fatalf("distinct output index lost tool identity: %q", got)
	}
	observations.SetToolInput(&generation, "", "main", GenerationItem{Index: 1, Indexed: true}, false)
	if got := observations.ToolInput("main"); got != "Emitting a patch" {
		t.Fatalf("index completion cleared a neighboring tool: %q", got)
	}
	observations.SetToolInput(&generation, "", "main", GenerationItem{ID: "call"}, false)
	if observations.ToolInput("main") != "" {
		t.Fatal("ID-only completion retained indexed tool identity")
	}
}

func TestGenerationObservationsRemainRequestScoped(t *testing.T) {
	var observations Observations
	var first, second Generation
	observations.SetToolInput(&first, "", "main", GenerationItem{ID: "item"}, true)
	observations.SetToolInput(&second, "/workspace", "main", GenerationItem{ID: "item"}, true)
	observations.EndGeneration(&first)
	if !observations.EmittingToolInput("/workspace", "main") || observations.EmittingToolInput("/other", "main") || observations.EmittingToolInput("/workspace", "child") {
		t.Fatal("ending one request changed another request's generation scope")
	}
	observations.SetToolInput(&second, "/workspace", "main", GenerationItem{ID: "item"}, false)
	if observations.EmittingToolInput("/workspace", "main") {
		t.Fatal("completed tool input still blocks question answers")
	}
	observations.SetAssistantMessage(&first, "", "main", GenerationItem{ID: "item"}, true)
	message := observations.AssistantMessage("/workspace", "main")
	observations.SetAssistantMessage(&second, "", "main", GenerationItem{ID: "item"}, true)
	later := observations.AssistantMessage("/workspace", "main")
	observations.EndGeneration(&first)
	if message == later || observations.AwaitingMessage("/workspace", "main", message) || !observations.AwaitingMessage("/workspace", "main", later) {
		t.Fatal("message boundaries lost request identity or rearmed after completion")
	}
}

func TestGenerationIdentityEnrichmentPreservesBoundary(t *testing.T) {
	var observations Observations
	var generation Generation
	indexed := GenerationItem{Index: 0, Indexed: true}
	identified := GenerationItem{ID: "message", Index: 0, Indexed: true}
	observations.IdentifyGenerationItem(&generation, identified)
	if observations.AssistantMessage("", "main") != 0 {
		t.Fatal("identity observation admitted a message")
	}
	observations.SetAssistantMessage(&generation, "", "main", indexed, true)
	boundary := observations.AssistantMessage("", "main")
	observations.IdentifyGenerationItem(&generation, identified)
	if boundary == 0 || observations.AssistantMessage("", "main") != boundary || !observations.AwaitingMessage("", "main", boundary) {
		t.Fatal("identity enrichment replaced or released the admitted boundary")
	}
	observations.SetAssistantMessage(&generation, "", "main", GenerationItem{ID: "message"}, false)
	observations.IdentifyGenerationItem(&generation, identified)
	if observations.AwaitingMessage("", "main", boundary) || observations.AssistantMessage("", "main") != 0 {
		t.Fatal("late identity revived a completed boundary")
	}
}

func TestApprovalFeedbackFollowsSuccessfulDelivery(t *testing.T) {
	var observations Observations
	failure := errors.New("answer rejected")
	if err := observations.AnswerWithFeedback("main", "turn", "undelivered", func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("delivery error = %v", err)
	}
	if observations.Feedback("main", "turn") != "" {
		t.Fatal("failed answer published feedback")
	}
	if err := observations.AnswerWithFeedback("main", "turn", "reason", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if observations.Feedback("main", "turn") != "reason" || observations.Feedback("child", "turn") != "" {
		t.Fatal("feedback was lost or escaped its thread")
	}
	observations.ClearFeedback("main", "other")
	if observations.Feedback("main", "turn") != "reason" {
		t.Fatal("unrelated turn removed feedback")
	}
	observations.ClearFeedback("main", "turn")
	if observations.Feedback("main", "turn") != "" {
		t.Fatal("completed turn retained feedback")
	}
}

func TestContextIdentityTracksHistoryReplacement(t *testing.T) {
	var observed Observations
	initial := observed.ContextIdentity("root")
	if initial == "" || observed.ContextIdentity("root") != initial || observed.ContextIdentity("child") == initial {
		t.Fatal("context identity is unstable or shared between threads")
	}
	observed.RefreshContext("root")
	refreshed := observed.ContextIdentity("root")
	if refreshed == initial {
		t.Fatal("rewind or reset reused the old history identity")
	}
	observed.ObserveCompactionContext("root", "window")
	compacted := observed.ContextIdentity("root")
	observed.ObserveCompactionContext("root", "window")
	if compacted == refreshed || compacted != observed.ContextIdentity("root") {
		t.Fatal("native window did not invalidate exactly once")
	}
	if new(Observations).ContextIdentity("root") == initial || (*Observations)(nil).ContextIdentity("root") != "" {
		t.Fatal("cold or missing owner reused a context")
	}
}
