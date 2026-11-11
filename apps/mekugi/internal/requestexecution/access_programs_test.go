package requestexecution

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
)

func TestRequestCyberAccessOptOut(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-astra", "gpt-6-luna"} {
		for _, raw := range []string{"", "null", `{}`, `{"cyber":"daybreak_blue"}`, `{"cyber":"daybreak_red","other":{"id":9007199254740993}}`, `{"cyber":"standard"}`} {
			t.Run(model+"/"+raw, func(t *testing.T) {
				request := modelTestRequest(t, model)
				if raw != "" {
					request.Fields["access_programs"] = []byte(raw)
				}
				input := bytes.Clone(request.Fields["input"])
				attempt := newRequestAttempt(Executor{}, t.Context(), t.Context(), request, http.Header{}, "")
				if err := attempt.prepareWire(); err != nil {
					t.Fatal(err)
				}
				var sent map[string]jsontext.Value
				if err := json.Unmarshal(attempt.forwardBody, &sent); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(sent["input"], input) || string(sent["model"]) != `"`+model+`"` {
					t.Fatal("opt-out changed input or model")
				}
				var programs map[string]jsontext.Value
				if err := json.Unmarshal(sent["access_programs"], &programs); err != nil {
					t.Fatal(err)
				}
				if string(programs["cyber"]) != `"standard"` {
					t.Fatalf("cyber program = %s", programs["cyber"])
				}
				if bytes.Contains([]byte(raw), []byte(`"other"`)) && string(programs["other"]) != `{"id":9007199254740993}` {
					t.Fatalf("other program changed: %s", programs["other"])
				}
			})
		}
	}
}

func TestNativeProviderDoesNotInjectOpenAIPolicy(t *testing.T) {
	for _, model := range []string{"gpt-6-sol"} {
		for _, sockets := range []bool{false, true} {
			provider := inferencegateway.NewClient("http://unused.invalid", nil, ProviderDiagnostics())
			provider.Native = &inferencegateway.NativeProvider{ID: "custom", WebSockets: sockets}
			request := modelTestRequest(t, model)
			attempt := newRequestAttempt(Executor{Provider: provider}, t.Context(), t.Context(), request, http.Header{}, "")
			if err := attempt.prepareWire(); err != nil {
				t.Fatal(err)
			}
			var sent map[string]jsontext.Value
			if err := json.Unmarshal(attempt.forwardBody, &sent); err != nil {
				t.Fatal(err)
			}
			if len(sent["access_programs"]) != 0 || string(sent["model"]) != `"`+model+`"` {
				t.Fatalf("native policy/model changed: %s", attempt.forwardBody)
			}
			if inferencegateway.NativeContinuations(provider) != sockets {
				t.Fatal("native ancestry followed model prefix")
			}
		}
	}
}
