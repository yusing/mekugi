package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Exercise the provider wire, not the locally reconstructed tool catalog.
func TestWebSocketPrewarmToolGuidanceDelivery(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	proxy := newManagedMekugiProxy(t)
	const conflictingProgress = "As you work, you send messages to the `commentary` channel."
	stock, _ := recordedInstructionCleanup(t)
	base := []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]string{"type": "message", "role": "developer", "content": "Follow the task.\n" + conflictingProgress + "\n" + stock}}
	incoming := []any{base[0], map[string]string{"type": "message", "role": "developer", "content": "Follow the task.\n" + conflictingProgress + "\n" + stock + "\n" + instructionOmitStart + "omitted-rtk-policy" + instructionOmitEnd}}
	ids := []string{"warm", "turn", "next", "astra", "astra-next"}
	headers := codexAuthHeaders()
	headers.Set(codexresponses.RoutingSessionHeader, "instruction-cache-session")
	directory := t.TempDir()
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		upstream.SetReadLimit(codexresponses.MaxResponseBytes)
		defer upstream.CloseNow()
		var warmedTools json.RawMessage
		var warmedInstructions string
		var observed string
		for index, id := range ids {
			request, err := providerSocketRead(ctx, upstream)
			if err != nil {
				t.Error(err)
				return
			}
			if bytes.Contains(request["input"], []byte("omitted-rtk-policy")) || bytes.Contains(request["input"], []byte("mekugi:omit")) || bytes.Contains(request["input"], []byte(conflictingProgress)) || bytes.Contains(request["input"], []byte("recommended_plugins")) {
				t.Error("provider received omitted or conflicting instructions")
			}
			if id != "warm" {
				facts := environmentFacts(t, responses.MustMarshalJSON(request))
				base, _, _ := strings.Cut(responses.StringField(request, "instructions"), "\n\n<mekugi_environment_facts>")
				if base != warmedInstructions {
					t.Error("continuation changed or lost base workflow")
				}
				if observed != "" && observed != facts {
					t.Error("ordinary continuation or model switch rescanned facts")
				}
				observed = facts
			}
			var input []json.RawMessage
			_ = json.Unmarshal(request["input"], &input)
			switch id {
			case "warm":
				warmedTools = bytes.Clone(request["tools"])
				warmedInstructions = responses.StringField(request, "instructions")
				if responses.StringField(request, "instructions") != proxy.registry.FrontendGuidance.Workflow {
					t.Error("prewarm did not deliver workflow in base instructions")
				}
				if bytes.Contains(request["input"], []byte("Keep command output in tool results")) {
					t.Error("workflow remains in tool description")
				}
				if string(request["generate"]) != "false" ||
					!bytes.Contains(request["input"], []byte("mekugi-frontends:start")) ||
					!bytes.Contains(request["input"], []byte("tools.exec_command")) ||
					bytes.Contains(request["tools"], []byte(`"shell"`)) {
					t.Errorf("prewarm projection: generate=%s frontends=%t stock=%t shell=%t",
						request["generate"],
						bytes.Contains(request["input"], []byte("mekugi-frontends:start")),
						bytes.Contains(request["input"], []byte("tools.exec_command")), bytes.Contains(request["tools"], []byte(`"shell"`)))
				}
			case "turn":
				if !responses.SameJSONValue(warmedTools, request["tools"]) {
					t.Error("first turn changed the warmed tool catalog")
				}
				if responses.StringField(request, "previous_response_id") != "warm" || len(input) != 1 {
					t.Errorf("first turn discarded warmed prefix: parent=%q items=%d", responses.StringField(request, "previous_response_id"), len(input))
				}
			case "next", "astra", "astra-next":
				if responses.StringField(request, "previous_response_id") != ids[index-1] || len(input) != 1 {
					t.Errorf("%s unnecessarily resent unchanged history", id)
				}
				if !responses.SameJSONValue(warmedTools, request["tools"]) {
					t.Errorf("%s changed model-independent tool guidance", id)
				}
			}
			if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", id)); err != nil {
				t.Error(err)
				return
			}
		}
		_, _, _ = upstream.Read(ctx)
	}), proxy, headers)
	for index, id := range ids {
		model := "gpt-5.6-luna"
		if index >= 3 {
			model = "gpt-6-astra"
		}
		request := map[string]any{"type": "response.create", "model": model}
		metadata := codexresponses.TurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{directory: nil}}
		if index == 0 {
			metadata.RequestKind = "prewarm"
			request["generate"] = false
			request["input"] = incoming
		} else {
			request["previous_response_id"] = ids[index-1]
			request["input"] = []any{map[string]string{"role": "user", "content": id}}
		}
		request["client_metadata"] = map[string]string{codexresponses.TurnMetadataHeader: string(responses.MustMarshalJSON(metadata)), codexresponses.ThreadIDHeader: "instruction-cache-thread"}
		socketWrite(t, ctx, conn, request)
		if event := socketRead(t, ctx, conn); responses.StringField(event, "type") != "response.completed" {
			t.Fatalf("%s failed: %s", id, responses.MustMarshalJSON(event))
		}
	}
}

// Provider history reuses only confirmed leading conversation items. Lazily
// acquired environment data stays in the independently supplied base layer.

func TestInstructionCacheAutomaticSuccessorCannotSilentlyDropChanges(t *testing.T) {
	original := json.RawMessage(`{"role":"developer","content":"rules"}`)
	exchange := confirmedHistoryExchange(t, []json.RawMessage{original}, true)
	request := &codexresponses.Request{Fields: map[string]json.RawMessage{
		"previous_response_id": json.RawMessage(`"parent"`),
		"input":                responses.MustMarshalJSON([]json.RawMessage{original}),
	}}
	if reason, err := exchange.Reconcile(request, true); err != nil || reason != "matching_prefix" || request.CachedInput != 1 {
		t.Fatalf("confirmed automatic prefix = %q, %v; cached=%d", reason, err, request.CachedInput)
	}
	request.Fields["input"] = json.RawMessage(`[{"role":"developer","content":"changed"}]`)
	_, err := exchange.Reconcile(request, true)
	if err == nil || !strings.Contains(err.Error(), "cached provider history") {
		t.Fatalf("automatic successor silently accepted a different prefix: %v", err)
	}
}

func TestContextUsageNoticeProviderPrefix(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":"task"},{"type":"function_call_output","call_id":"call","output":"done"}]`)
	admitted := binding.ContextUsage{Session: t.Name(), Thread: "main", Tokens: 500, Window: 1000}
	var warnings []string
	admitted.Notify = func(text string) { warnings = append(warnings, text) }
	admitted.FreshInput = func() json.RawMessage { return raw }
	project := testGuidance().ContextUsage
	update, err := project(binding.InstructionInput{Input: raw}, admitted)
	var sent []json.RawMessage
	if err != nil || json.Unmarshal(update.Input, &sent) != nil || len(sent) != 3 || !bytes.Contains(sent[2], []byte("Mekugi context usage: 50%")) || !responses.SameJSONValue(sent[1], json.RawMessage(`{"type":"function_call_output","call_id":"call","output":"done"}`)) {
		t.Fatalf("tool notice projection: %+v, %v", update, err)
	}
	for _, automatic := range []bool{false, true} {
		admitted.Tokens, admitted.Window = 0, 0
		request := new(codexresponses.Request)
		exchange := confirmedHistoryExchange(t, sent[:2], automatic, func(next *codexresponses.Request) {
			replayed, err := project(binding.InstructionInput{Input: next.Fields["input"]}, admitted)
			if err != nil {
				t.Fatal(err)
			}
			next.Fields["input"] = replayed.Input
			*request = *next
		})

		if reason, err := exchange.Reconcile(request, true); err != nil || reason != "matching_prefix" || request.CachedInput != 3 || request.RebaseInput {
			t.Fatalf("notice prefix: %s, %v, cached=%d", reason, err, request.CachedInput)
		}
	}
	admitted.Tokens, admitted.Window, admitted.Compactions = 700, 1000, 1
	update, err = project(binding.InstructionInput{Input: raw}, admitted)
	if err != nil || bytes.Count(update.Input, []byte("Mekugi context usage:")) != 1 || !bytes.Contains(update.Input, []byte("70%")) {
		t.Fatalf("compaction cadence: %+v, %v", update, err)
	}
	if len(warnings) != 2 || warnings[0] != "Mekugi context usage: 50% (500/1000 tokens)." || warnings[1] != "Mekugi context usage: 70% (700/1000 tokens)." {
		t.Fatalf("fresh warnings or silent replay: %v", warnings)
	}

	for _, window := range []uint64{0, 1000} {
		update, err := project(binding.InstructionInput{Input: []byte("not JSON")}, binding.ContextUsage{Session: t.Name(), Thread: "unused", Tokens: 499, Window: window, FreshInput: func() json.RawMessage { t.Fatal("unneeded fresh input encoded"); return nil }})
		if err != nil || update.InputChanged {
			t.Fatalf("unneeded history decoded: %v", err)
		}
	}
}
