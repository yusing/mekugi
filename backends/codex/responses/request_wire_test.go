package responses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestRequestWireBodyPreservesUnchangedBytes(t *testing.T) {
	for _, body := range []string{
		" \n{ \"model\" : \"model\", \"input\": \"<hello>&\\u0061\", \"stream\" : false } \n",
		`{"model":"first","model":"last","input": "<a>"}`,
		" { } \n",
	} {
		request, err := ParseRequest([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		got, err := request.WireBody(request.Fields)
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
	request, err := ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Fields["model"] = json.RawMessage(`"new"`)
	request.Fields["instructions"] = json.RawMessage(`"<native>&"`)
	got, err := request.WireBody(request.Fields)
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
		r, err := ParseRequest([]byte(empty))
		if err != nil {
			t.Fatal(err)
		}
		r.Fields["model"] = json.RawMessage(`"new"`)
		encoded, err := r.WireBody(r.Fields)
		want := strings.Replace(empty, "}", `"model":"new"}`, 1)
		if err != nil || string(encoded) != want {
			t.Fatalf("added field: %s, %v", encoded, err)
		}
	}
}

func TestRequestWirePreservesSemanticallyUnchangedProjection(t *testing.T) {
	body := []byte(` { "input": [ { "content":"<>&", "role" : "user" } ], "instructions":"native" } `)
	request, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	request.Fields["input"] = json.RawMessage(`[{"role":"user","content":"\u003c\u003e\u0026"}]`)
	got, err := request.WireBody(request.Fields)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("unchanged projected value rewritten: %s, %v", got, err)
	}
}

func TestPromptCacheKeyRejectsUnsafeRoutingValues(t *testing.T) {
	for _, key := range []string{" surrounding ", "line\nbreak", "line\rbreak", "control\x01", "delete\x7f"} {
		request, err := ParseRequest(responses.MustMarshalJSON(map[string]any{"model": "model", "prompt_cache_key": key}))
		if err != nil {
			t.Fatal(err)
		}
		if request.PromptCacheKey() != "" {
			t.Fatal("unsafe prompt cache key suppressed the valid session fallback")
		}
	}
}
