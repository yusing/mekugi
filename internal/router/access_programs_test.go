package router

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"testing"
)

func TestRequestCyberAccessOptOut(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-astra", "gpt-6-luna", grokModel, "opencode-go:test"} {
		for _, raw := range []string{"", "null", `{}`, `{"cyber":"daybreak_blue"}`, `{"cyber":"daybreak_red","other":{"id":9007199254740993}}`, `{"cyber":"standard"}`} {
			t.Run(model+"/"+raw, func(t *testing.T) {
				request := mentorTestRequest(t, model)
				if raw != "" {
					request.fields["access_programs"] = []byte(raw)
				}
				input := bytes.Clone(request.fields["input"])
				attempt := newRequestAttempt(requestExecutor{provider: &serverFakeProvider{}}, t.Context(), t.Context(), request, http.Header{}, "")
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
				if isGrokModel(model) || isOpenCodeModel(model) {
					if string(sent["access_programs"]) != raw {
						t.Fatalf("third-party programs changed: %s", sent["access_programs"])
					}
					return
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
