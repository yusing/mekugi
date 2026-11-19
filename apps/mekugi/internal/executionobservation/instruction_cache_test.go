package executionobservation

import (
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestPrewarmUnsupportedCatalogRemainsNative(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{"model": "gpt-test", "generate": false, "instructions": testBaseInstructions, "tools": []any{map[string]string{"type": "web_search"}}, "input": []any{}}))
	if err != nil {
		t.Fatal(err)
	}
	before := responses.MustMarshalJSON(request.Fields)
	transform, err := proxy.prepareModelRequest(t.Context(), &request, "", "", codexresponses.TurnMetadata{RequestKind: "prewarm"}, true, true)
	if err != nil || transform != nil {
		t.Fatalf("native handshake initialized execution or failed: %v, %v", transform, err)
	}
	if !responses.SameJSONValue(before, responses.MustMarshalJSON(request.Fields)) {
		t.Fatal("unsupported prewarm catalog was partially rewritten")
	}
}

func TestPrewarmMalformedCatalogRejectsWithoutPanic(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	for _, catalog := range []string{`[42]`, `"invalid"`, `[{"type":"function","name":"lookup"},42]`} {
		request, err := codexresponses.ParseRequest([]byte(`{"model":"gpt-test","generate":false,"tools":` + catalog + `,"input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := proxy.prepareModelRequest(t.Context(), &request, "", "", codexresponses.TurnMetadata{RequestKind: "prewarm"}, true, true); err == nil {
			t.Fatalf("malformed catalog accepted: %s", catalog)
		}
	}
}
