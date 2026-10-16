package responses

import (
	"bytes"
	"encoding/json/jsontext"
	"testing"

	protocol "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestRequestEnvelopePermissiveWireAcceptance(t *testing.T) {
	body := []byte(" {\"model\":\"first\",\"model\":\" final\xff \",\"stream\":false,\"stream\":true," +
		`"reasoning":{"effort":"low","effort":" high "},"prompt_cache_key":"session",` +
		`"input":[{"n":9007199254740993,"decimal":1.00,"exponent":1e3}],"opaque":"` + "\xff\"} ")
	r, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model() != "final\ufffd" || !r.Stream || r.ReasoningEffort() != "high" || r.PromptCacheKey() != "session" {
		t.Fatalf("decoded envelope: model=%q stream=%v effort=%q cache=%q", r.Model(), r.Stream, r.ReasoningEffort(), r.PromptCacheKey())
	}
	const input = `[{"n":9007199254740993,"decimal":1.00,"exponent":1e3}]`
	if string(r.Fields["input"]) != input {
		t.Fatalf("numeric spelling lost: %s", r.Fields["input"])
	}
	got, err := r.WireBody(r.Fields)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("wire round trip: %q, %v", got, err)
	}
	r.Fields["input"] = jsontext.Value(`[]`)
	if string(r.OriginalField("input")) != input {
		t.Fatalf("projection changed original input: %s", r.OriginalField("input"))
	}
}

func TestRequestEnvelopeRejectsInvalidBodies(t *testing.T) {
	for _, body := range []string{"", `null`, `[]`, `true`, `1`, `"model"`, `{`, `{} {}`, `{} null`, `{} garbage`, `{"background":true}`} {
		t.Run(body, func(t *testing.T) {
			if _, err := ParseRequest([]byte(body)); err == nil {
				t.Fatalf("accepted %q", body)
			}
		})
	}
}

func TestRequestIncrementalBodyAcceptance(t *testing.T) {
	const body = ` { "input":[{"n":9007199254740993,"v":1.00},{"role":"user","content":"next"}], "previous_response_id":"resp-1", "model":"model" } `
	for _, tt := range []struct {
		name      string
		body      string
		cached    int
		rebase    bool
		useCache  bool
		wantInput string
		wantErr   bool
	}{
		{name: "no cache boundary", body: body, useCache: true},
		{name: "provider cache disabled", body: body, cached: 3, rebase: true},
		{name: "cached prefix", body: body, cached: 1, useCache: true, wantInput: `[{"role":"user","content":"next"}]`},
		{name: "entire prefix", body: body, cached: 2, useCache: true, wantInput: `[]`},
		{name: "rebase without prefix", body: body, rebase: true, useCache: true, wantInput: `[{"n":9007199254740993,"v":1.00},{"role":"user","content":"next"}]`},
		{name: "rebase cached prefix", body: body, cached: 1, rebase: true, useCache: true, wantInput: `[{"role":"user","content":"next"}]`},
		{name: "boundary exceeds input", body: body, cached: 3, useCache: true, wantErr: true},
		{name: "nonarray input", body: `{"input":"text"}`, cached: 1, useCache: true, wantErr: true},
		{name: "missing input", body: `{}`, cached: 1, useCache: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ParseRequest([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			r.CachedInput, r.RebaseInput = tt.cached, tt.rebase
			wire, err := r.WireBody(r.Fields)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.IncrementalBody(wire, tt.useCache)
			if (err != nil) != tt.wantErr {
				t.Fatalf("IncrementalBody error = %v, want error %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.wantInput == "" {
				if !bytes.Equal(got, wire) {
					t.Fatalf("uncached body changed: %s", got)
				}
				return
			}
			forwarded, err := ParseRequest(got)
			if err != nil {
				t.Fatal(err)
			}
			if !protocol.SameJSONValue(forwarded.Fields["input"], []byte(tt.wantInput)) || forwarded.Model() != "model" {
				t.Fatalf("forwarded request: %s", got)
			}
			_, hasPrevious := forwarded.Fields["previous_response_id"]
			if hasPrevious == tt.rebase {
				t.Fatalf("previous response retention: %s", got)
			}
			if !bytes.Equal(r.OriginalField("input"), r.Fields["input"]) {
				t.Fatal("incremental send changed prepared input")
			}
		})
	}
}
