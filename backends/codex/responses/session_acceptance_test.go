package responses_test

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/backends/codex/responses"
)

func sessionJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func sessionPrepare(t *testing.T, history *responses.SessionHistory, command, event string) (*responses.SessionExchange, responses.Request) {
	t.Helper()
	exchange, request, err := history.Prepare([]byte(command), []byte(event))
	if err != nil {
		t.Fatal(err)
	}
	return exchange, request
}

func sessionWantJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var actual, expected any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

// Observe raw provider events before exposing the same events to native history.
func sessionObserve(t *testing.T, exchange *responses.SessionExchange, event string) {
	t.Helper()
	if err := exchange.ObserveProvider([]byte(event)); err != nil {
		t.Fatal(err)
	}
	if err := exchange.ObserveOutput([]byte(event)); err != nil {
		t.Fatal(err)
	}
}

func TestSessionAcceptanceReconstruction(t *testing.T) {
	var history responses.SessionHistory
	parent, request := sessionPrepare(t, &history, `{"type":"response.create","model":"client-model","reasoning":{"effort":"low"},"instructions":"rules","tools":[],"generate":false,"input":"first"}`, "")
	if parent.Automatic() || !request.Stream || string(request.Fields["generate"]) != "false" {
		t.Fatalf("explicit prewarm: automatic=%v stream=%v fields=%s", parent.Automatic(), request.Stream, sessionJSON(t, request.Fields))
	}
	sessionWantJSON(t, request.Fields["input"], `[{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}]`)
	// Provider selection may differ from the native command's model contract.
	request.Fields["model"] = jsontext.Value(`"provider-model"`)
	request.Fields["reasoning"] = jsontext.Value(`{"effort":"high","summary":"auto"}`)
	parent.RememberProviderModel(request)
	if err := parent.BeginProvider(request.Fields); err != nil {
		t.Fatal(err)
	}
	sessionObserve(t, parent, `{"type":"response.created","response":{"id":"parent"}}`)
	sessionObserve(t, parent, `{"type":"response.completed","response":{"id":"parent","output":[{"type":"message","role":"assistant","content":"answer"}]}}`)

	for _, event := range []string{`{"type":"response.created","response":{}}`, `{"type":"response.created","response":{"previous_response_id":"parent"}}`} {
		automatic, next := sessionPrepare(t, &history, "", event)
		if !automatic.Automatic() || next.Model() != "client-model" || next.ReasoningEffort() != "low" {
			t.Fatalf("automatic native settings = %s", sessionJSON(t, next.Fields))
		}
		for _, key := range []string{"generate", "type"} {
			if _, exists := next.Fields[key]; exists {
				t.Fatalf("automatic inherited %s", key)
			}
		}
		sessionWantJSON(t, next.Fields["instructions"], `"rules"`)
		sessionWantJSON(t, next.Fields["tools"], `[]`)
		sessionWantJSON(t, next.Fields["previous_response_id"], `"parent"`)
		if err := automatic.RestoreProviderModel(&next); err != nil {
			t.Fatal(err)
		}
		if next.Model() != "provider-model" {
			t.Fatalf("automatic provider model = %q", next.Model())
		}
		sessionWantJSON(t, next.Fields["reasoning"], `{"effort":"high","summary":"auto"}`)
		reason, err := automatic.Reconcile(&next, true)
		if err != nil || reason != "matching_prefix" || next.CachedInput != 2 || next.RebaseInput {
			t.Fatalf("automatic reconciliation = %q, %v; cached=%d rebase=%v", reason, err, next.CachedInput, next.RebaseInput)
		}
	}

	for _, input := range []string{`null`, `[]`, `[{"type":"function_call_output","call_id":"call","output":"result"}]`} {
		explicit, next := sessionPrepare(t, &history, `{"type":"response.create","previous_response_id":"parent","model":"new-model","input":`+input+`}`, "")
		if explicit.Automatic() {
			t.Fatal("explicit continuation became automatic")
		}
		if err := explicit.RestoreProviderModel(&next); err != nil || next.Model() != "new-model" {
			t.Fatalf("explicit model was replaced: %q, %v", next.Model(), err)
		}
		for _, key := range []string{"reasoning", "instructions", "tools", "generate", "type"} {
			if _, exists := next.Fields[key]; exists {
				t.Fatalf("explicit continuation inherited %s", key)
			}
		}
		want := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},{"type":"message","role":"assistant","content":"answer"}`
		if input != "null" && input != "[]" {
			want += `,` + input[1:len(input)-1]
		}
		sessionWantJSON(t, next.Fields["input"], want+`]`)
	}
}

func TestSessionAcceptanceInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name, command, event string
		invalid              bool
	}{
		{"malformed command", `{`, "", true},
		{"multiplexed lane", `{"stream_id":"lane","input":[]}`, "", true},
		{"invalid input", `{"input":42}`, "", true},
		{"explicit missing parent", `{"previous_response_id":"missing","input":[]}`, "", true},
		{"automatic missing parent", "", `{"response":{"previous_response_id":"missing"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var history responses.SessionHistory
			_, _, err := history.Prepare([]byte(tc.command), []byte(tc.event))
			var invalid *responses.InvalidRequestError
			if err == nil || errors.As(err, &invalid) != tc.invalid {
				t.Fatalf("error = %v (%T), want InvalidRequestError=%v", err, err, tc.invalid)
			}
		})
	}
	var history responses.SessionHistory
	if _, _, err := history.Prepare([]byte(`{"stream_id":null,"input":null}`), nil); err != nil {
		t.Fatalf("null lane rejected: %v", err)
	}
}

func TestSessionAcceptanceLateSteeringAdmission(t *testing.T) {
	var history responses.SessionHistory
	parent, request := sessionPrepare(t, &history, `{"model":"model","input":[{"role":"user","content":"first"}]}`, "")
	parent.RememberProviderModel(request)
	if err := parent.BeginProvider(request.Fields); err != nil {
		t.Fatal(err)
	}
	sessionObserve(t, parent, `{"type":"response.completed","response":{"id":"parent","output":[]}}`)
	for _, content := range []string{"rejected", "steer"} {
		if err := history.AddSteering(map[string]jsontext.Value{
			"previous_response_id": jsontext.Value(`"parent"`),
			"input":                jsontext.Value(`[{"role":"user","content":"` + content + `"}]`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	history.ObserveControl([]byte(`{"type":"response.steer.failed","steer":{"previous_response_id":"parent"}}`))
	next, request := sessionPrepare(t, &history, `{"model":"model","previous_response_id":"parent","input":[{"role":"user","content":"next"}]}`, "")
	reason, err := next.Reconcile(&request, true)
	if err != nil || reason != "matching_prefix" || request.CachedInput != 1 {
		t.Fatalf("continuation reconciliation = %q, %v; cached=%d", reason, err, request.CachedInput)
	}
	body, err := request.IncrementalBody(sessionJSON(t, request.Fields), true)
	if err != nil {
		t.Fatal(err)
	}
	providerRequest, err := responses.ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	sessionWantJSON(t, providerRequest.Fields["input"], `[{"role":"user","content":"next"}]`)
	next.RememberProviderModel(request)
	if err := next.BeginProvider(providerRequest.Fields); err != nil {
		t.Fatal(err)
	}
	// Acknowledgement arrives after preparation and sending, before admission.
	history.ObserveControl([]byte(`{"type":"response.steer.accepted","steer":{"id":"accepted","previous_response_id":"parent"}}`))
	sessionObserve(t, next, `{"type":"response.created","response":{"id":"next"}}`)
	sessionObserve(t, next, `{"type":"response.completed","response":{"id":"next","output":[]}}`)
	automatic, request := sessionPrepare(t, &history, "", `{"response":{}}`)
	sessionWantJSON(t, request.Fields["input"], `[{"role":"user","content":"first"},{"role":"user","content":"steer"},{"role":"user","content":"next"}]`)
	reason, err = automatic.Reconcile(&request, true)
	if err != nil || reason != "matching_prefix" || request.CachedInput != 3 {
		t.Fatalf("admitted reconciliation = %q, %v; cached=%d", reason, err, request.CachedInput)
	}
	// Consumed steering must not be replayed against its original parent.
	_, branch := sessionPrepare(t, &history, `{"previous_response_id":"parent","input":[]}`, "")
	sessionWantJSON(t, branch.Fields["input"], `[{"role":"user","content":"first"}]`)
}
