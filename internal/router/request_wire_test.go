package router

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRequestWireBodyPreservesUnchangedBytes(t *testing.T) {
	for _, body := range []string{
		" \n{ \"model\" : \"model\", \"input\": \"<hello>&\\u0061\", \"stream\" : false } \n",
		`{"model":"first","model":"last","input": "<a>"}`,
		" { } \n",
	} {
		request, err := parseResponsesRequest([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		got, err := request.wireBody(request.fields)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body {
			t.Fatalf("got %s want %s", got, body)
		}
	}
}

func TestRequestWireBodyReplacesOnlyProjectedFields(t *testing.T) {
	body := ` { "model":"first", "model" : "last", "input" : "<unchanged>&\u0061", "stream": false } `
	request, err := parseResponsesRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	request.fields["model"] = json.RawMessage(`"new"`)
	request.fields["instructions"] = json.RawMessage(`"<native>&"`)
	got, err := request.wireBody(request.fields)
	if err != nil {
		t.Fatal(err)
	}
	want := ` { "model":"new", "model" : "new", "input" : "<unchanged>&\u0061", "stream": false,"instructions":"<native>&" } `
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if string(request.originalBody) != body {
		t.Fatal("original request mutated")
	}
	for _, empty := range []string{"{}", " { \n } "} {
		r, err := parseResponsesRequest([]byte(empty))
		if err != nil {
			t.Fatal(err)
		}
		r.fields["model"] = json.RawMessage(`"new"`)
		encoded, err := r.wireBody(r.fields)
		if err != nil || !json.Valid(encoded) {
			t.Fatalf("added field: %s, %v", encoded, err)
		}
	}
}

func TestProtocolJSONDoesNotEscapeHTML(t *testing.T) {
	got, err := marshalProtocolJSON(map[string]any{"text": "<tag>&", "nested": map[string]string{"text": "<&>"}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte(`\u00`)) || !bytes.Contains(got, []byte(`<tag>&`)) {
		t.Fatalf("HTML escaped: %s", got)
	}
}

func TestRequestWirePreservesSemanticallyUnchangedProjection(t *testing.T) {
	body := []byte(` { "input": [ { "content":"<>&", "role" : "user" } ], "instructions":"native" } `)
	request, err := parseResponsesRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	request.fields["input"] = json.RawMessage(`[{"role":"user","content":"\u003c\u003e\u0026"}]`)
	got, err := request.wireBody(request.fields)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("unchanged projected value rewritten: %s, %v", got, err)
	}
}

func TestPromptCacheKeyRejectsUnsafeRoutingValues(t *testing.T) {
	for _, key := range []string{" surrounding ", "line\nbreak", "line\rbreak", "control\x01", "delete\x7f"} {
		request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": "model", "prompt_cache_key": key}))
		if err != nil {
			t.Fatal(err)
		}
		if request.promptCacheKey() != "" {
			t.Fatal("unsafe prompt cache key suppressed the valid session fallback")
		}
	}
}
