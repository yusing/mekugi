package requestexecution

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

func testInstructionRetention(t testing.TB) (*diagnostic.Service, *diagnostic.InstructionRetention, string) {
	t.Helper()
	historyService, err := history.NewService(diagnostic.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := query.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := diagnostic.New(historyService, inspection, nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	store, err := service.OpenFailures(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	return service, service.RetainInstructions(store, nil, history.Limits{}), directory
}

func TestRetainInstructionsKeepsForwardedProjection(t *testing.T) {
	service, retention, directory := testInstructionRetention(t)
	request, err := codexresponses.ParseRequest([]byte(`{"model":"model-a","instructions":"projected <mekugi_environment_facts>facts</mekugi_environment_facts>","tools":[{"name":"exec"}],"input":[{"type":"message","role":"developer","content":"rules"},{"type":"additional_tools","tools":[{"name":"extra"}]},{"role":"user","content":"excluded user"},{"type":"function_call_output","call_id":"c","output":"excluded output"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, thread           string
		prewarm, approval, use bool
	}{
		{name: "prewarm", thread: "prewarm", prewarm: true},
		{name: "approval review", thread: "review", approval: true},
		{name: "turn", thread: "turn", use: true},
	} {
		attempt := newRequestAttempt(Executor{Instructions: retention, Issues: testCriticalErrors()}, t.Context(), t.Context(), request, http.Header{}, "session")
		attempt.threadID, attempt.prewarm, attempt.approvalReview = tc.thread, tc.prewarm, tc.approval
		attempt.metadata.TurnID = "turn-1"
		attempt.retainInstructions()
		report, err := service.ReadInstructions(t.Context(), directory, tc.thread, true, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if !tc.use {
			if report.Status != diagnostic.InstructionsUnavailable {
				t.Fatalf("%s retained instructions: %+v", tc.name, report)
			}
			continue
		}
		if report.Status != diagnostic.InstructionsAvailable || len(report.Revisions) != 1 || report.Uses[0].TurnID != "turn-1" || report.Uses[0].Model != "model-a" {
			t.Fatalf("%s report = %+v", tc.name, report)
		}
		revision := report.Revisions[0]
		if len(revision.DeveloperMessages) != 1 || len(revision.Tools) != 1 || len(revision.AdditionalTools) != 1 {
			t.Fatalf("%s revision = %+v", tc.name, revision)
		}
		if got := report.Content[revision.Instructions].Text; got != "projected <mekugi_environment_facts>facts</mekugi_environment_facts>" {
			t.Fatalf("%s instructions = %q", tc.name, got)
		}
		if got := report.Content[revision.DeveloperMessages[0]].Text; got != `{"content":"rules","role":"developer","type":"message"}` {
			t.Fatalf("%s developer item = %q", tc.name, got)
		}
		for _, text := range report.Content {
			if strings.Contains(text.Text, "excluded") {
				t.Fatalf("%s retained excluded input: %q", tc.name, text.Text)
			}
		}
	}
}

type stoppedProvider struct{}

func (stoppedProvider) ForwardExecution(context.Context, context.Context, []byte, http.Header, string) (*inferencegateway.Response, error) {
	return nil, errors.New("provider stopped by fixture")
}

// The real request lifecycle captures instructions after every guidance
// projection, immediately before the provider is called.
func TestRetainInstructionsObservesGuidanceProjection(t *testing.T) {
	service, retention, directory := testInstructionRetention(t)
	request, err := codexresponses.ParseRequest([]byte(`{"model":"model-a","instructions":"base","input":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	project := func(marker string) func(binding.InstructionInput) (binding.InstructionUpdate, error) {
		return func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
			text, ok := responses.DecodeString(input.Instructions)
			if !ok {
				return binding.InstructionUpdate{}, errors.New("fixture instructions")
			}
			input.Instructions = responses.MustMarshalJSON(text + " " + marker)
			return binding.InstructionUpdate{InstructionInput: input, InstructionsChanged: true}, nil
		}
	}
	executor := Executor{
		Provider: stoppedProvider{}, Observations: new(lifecyclePreparation), Issues: testCriticalErrors(), Instructions: retention,
		Guidance: &binding.Guidance{
			BeforePreparation: project("<mekugi_environment_facts>facts</mekugi_environment_facts>"),
			AfterAttachments:  func(input binding.InstructionInput, _ bool) (binding.InstructionUpdate, error) { return project("guidance")(input) },
		},
	}
	headers := http.Header{}
	headers.Set(codexresponses.ThreadIDHeader, "thread")
	if err := executor.Execute(t.Context(), t.Context(), request, headers, "session"); err == nil || !strings.Contains(err.Error(), "provider stopped by fixture") {
		t.Fatalf("request did not reach the provider: %v", err)
	}
	report, err := service.ReadInstructions(t.Context(), directory, "thread", true, 4096)
	if err != nil || report.Status != diagnostic.InstructionsAvailable {
		t.Fatalf("report = %+v %v", report, err)
	}
	if got := report.Content[report.Revisions[0].Instructions].Text; got != "base <mekugi_environment_facts>facts</mekugi_environment_facts> guidance" {
		t.Fatalf("captured instructions = %q", got)
	}
}

func TestInstructionItemsTolerateUnrelatedItemsAndRejectPartialSelections(t *testing.T) {
	for _, input := range []string{
		`[{"role":"developer","content":"a"},{"role":"user","x":1,"x":2},{"role":"developer","content":"b"}]`,
		"[{\"role\":\"developer\",\"content\":\"a\"},{\"role\":\"user\",\"content\":\"\xff\"},{\"role\":\"developer\",\"content\":\"b\"}]",
		`[{"role":"developer","content":"a"},{"role":"user","content":"\ud800"},{"role":"developer","content":"b"}]`,
	} {
		if developers, _ := instructionItems([]byte(input)); len(developers) != 2 {
			t.Fatalf("input %q selected %d developer messages, want 2", input, len(developers))
		}
	}
	for _, input := range []string{``, `"text input"`, `null`, `{"role":"developer"}`, `[{"role":"developer"},{"role":`} {
		developers, additional := instructionItems([]byte(input))
		if len(developers) != 0 || len(additional) != 0 {
			t.Fatalf("input %q selected %v %v", input, developers, additional)
		}
	}
	// A malformed tools array discards the whole request rather than a prefix.
	if err := (instructionContent{"tools": []byte(`[{"name":"a"},{"name":`)}).visit(discardVisitor{}); err == nil {
		t.Fatal("malformed tools were accepted")
	}
}

type discardVisitor struct{}

func (discardVisitor) Visit(diagnostic.InstructionField, jsontext.Value) {}

func unchangedRetentionRequest(t testing.TB, items int) *requestAttempt {
	t.Helper()
	_, retention, _ := testInstructionRetention(t)
	var input []string
	filler := strings.Repeat("x", 1000)
	for i := range items {
		if i%200 == 0 {
			input = append(input, fmt.Sprintf(`{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev %d %s"}]}`, i, filler))
			continue
		}
		input = append(input, fmt.Sprintf(`{"type":"function_call_output","call_id":"c%d","output":"%s"}`, i, filler))
	}
	var tools []string
	for i := range 60 {
		tools = append(tools, fmt.Sprintf(`{"type":"function","name":"t%d","description":"%s","parameters":{"type":"object"}}`, i, strings.Repeat("d", 1500)))
	}
	request, err := codexresponses.ParseRequest([]byte(`{"model":"m","instructions":"` + strings.Repeat("i", 40000) + `","tools":[` + strings.Join(tools, ",") + `],"input":[` + strings.Join(input, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	attempt := newRequestAttempt(Executor{Instructions: retention, Issues: testCriticalErrors()}, context.Background(), context.Background(), request, http.Header{}, "session")
	attempt.threadID = "thread"
	attempt.retainInstructions()
	return attempt
}

// Unchanged requests allocate a constant amount regardless of history size.
func TestRetainInstructionsUnchangedAllocationsDoNotScale(t *testing.T) {
	small, large := unchangedRetentionRequest(t, 20), unchangedRetentionRequest(t, 2000)
	smallAllocs := testing.AllocsPerRun(20, small.retainInstructions)
	largeAllocs := testing.AllocsPerRun(20, large.retainInstructions)
	if largeAllocs > smallAllocs || largeAllocs > 16 {
		t.Fatalf("unchanged allocations: %v for 20 items, %v for 2000 items", smallAllocs, largeAllocs)
	}
}

func BenchmarkRetainInstructionsUnchanged(b *testing.B) {
	attempt := unchangedRetentionRequest(b, 2000)
	b.ReportAllocs()
	for b.Loop() {
		attempt.retainInstructions()
	}
}
