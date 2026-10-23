package launch

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Check final provider fields, including fresh request owners with no live parent.
func TestWorkflowInstructionsProviderDelivery(t *testing.T) {
	const start, end = "<!-- mekugi-workflow:start -->", "<!-- mekugi-workflow:end -->"
	const caller = "Caller-owned policy.\n\n"
	for _, tc := range []struct {
		name       string
		base       any
		supplied   bool
		wantPrefix string
	}{
		{name: "missing"},
		{name: "null", supplied: true},
		{name: "custom", base: caller, supplied: true, wantPrefix: caller + "\n\n"},
		{name: "refresh", base: caller + start + "obsolete" + end + "trailing policy", supplied: true, wantPrefix: caller},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			guide := proxy.registry.FrontendGuidance
			request := serverRequest(t, func(fields map[string]any) {
				if tc.supplied {
					fields["instructions"] = tc.base
				}
			})
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"id":"response","status":"completed","output":[]}`)}}}
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "fresh-session", provider, &output, nil, proxy); err != nil {
				t.Fatal(err)
			}
			got, err := codexresponses.ParseRequest(provider.forwarded[0])
			if err != nil {
				t.Fatal(err)
			}
			want := tc.wantPrefix + guide.Workflow
			if tc.name == "refresh" {
				want += "trailing policy"
			}
			if actual := responses.StringField(got.Fields, "instructions"); actual != want {
				t.Fatalf("base instructions = %q, want %q", actual, want)
			}
			catalog := codexresponses.DecodeToolCatalog(got.Fields)
			description := catalog.Additional[0].Tools.Tools[0].Description()
			if !strings.Contains(description, guide.Descriptions) || strings.Contains(description, start) || strings.Contains(description, "Keep command output in tool results") {
				t.Fatal("provider tool description did not separate capabilities from workflow")
			}
			if bytes.Contains(got.Fields["input"], []byte(start)) {
				t.Fatal("workflow leaked into conversation history")
			}
		})
	}
	for _, bad := range []string{start, end, end + start, start + start + end} {
		proxy := newManagedMekugiProxy(t)
		request := serverRequest(t, func(fields map[string]any) { fields["instructions"] = bad })
		provider := new(serverFakeProvider)
		var output bytes.Buffer
		err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "session", provider, &output, nil, proxy)
		if err == nil || len(provider.forwarded) != 0 {
			t.Fatal("malformed workflow markers reached provider")
		}
	}
}

func TestWorkflowInstructionsExecutionFree(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	request := serverRequest(t, func(fields map[string]any) {
		fields["instructions"] = "Caller-owned policy."
		fields["tools"] = []any{}
		fields["input"] = []any{map[string]any{"role": "user", "content": "native request"}}
	})
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"id":"native","status":"completed","output":[]}`)}}}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), "session", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	var got map[string]jsontext.Value
	if err := json.Unmarshal(provider.forwarded[0], &got); err != nil {
		t.Fatal(err)
	}
	if string(got["instructions"]) != string(request.Fields["instructions"]) || bytes.Contains(provider.forwarded[0], []byte("mekugi-workflow")) {
		t.Fatal("execution-free request gained workflow instructions")
	}
}
