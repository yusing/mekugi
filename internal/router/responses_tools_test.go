package router

import (
	"encoding/json"
	"testing"
)

func TestParsedResponsesRequestCachesToolCatalog(t *testing.T) {
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"tools": mustTestJSON(t, []any{map[string]any{"type": "function", "name": "lookup"}}),
	}}

	first := request.responseTools()
	second := request.responseTools()
	if first != second {
		t.Fatal("request decoded more than one tool catalog")
	}
	if got := second.top.tools[0].Name; got != "lookup" {
		t.Fatalf("tool name = %q", got)
	}
}
