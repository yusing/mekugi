package router

import (
	"encoding/json"
	"strings"
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

func TestToolCatalogMutationRetainsNestedDefinitions(t *testing.T) {
	fields := map[string]json.RawMessage{
		"tools": json.RawMessage(`[{"type":"function","name":"remove"},{"type":"namespace","name":"functions","tools":[{"type":"function","name":"keep","description":"original","extension":{"value":1}}]}]`),
	}
	catalog := decodeResponsesToolCatalog(fields)
	nested := catalog.top.tools[1].nested
	nested.tools[0].setDescription("updated")
	catalog.appendTop([]*responsesToolDefinition{newResponsesToolDefinition(map[string]json.RawMessage{
		"type": mustMarshalJSON("function"),
		"name": mustMarshalJSON("appended"),
	})})
	catalog.removeTop(0)
	if catalog.top.tools[0].nested != nested {
		t.Fatal("removing a sibling rebuilt the nested catalog")
	}
	projected, err := projectCTP2ToolSection(catalog.top, strings.ToUpper)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"keep","description":"UPDATED","extension":{"value":1}}]},{"type":"function","name":"appended"}]`
	if !sameJSONValue(projected, []byte(want)) {
		t.Fatalf("projected catalog = %s", projected)
	}
	if nested.tools[0].Description != "updated" {
		t.Fatal("projection mutated the source catalog")
	}
}

func TestToolCatalogProjectionPreservesMalformedEntries(t *testing.T) {
	raw := json.RawMessage(`[null,17,{"type":"namespace","name":"functions","tools":[null,false,{"type":"function","name":"keep","description":"original"}]}]`)
	section := decodeResponsesToolSection(raw, true)
	if section.err == nil || section.tools[2].nested.err == nil {
		t.Fatal("malformed entries must remain visible to strict consumers")
	}
	projected, err := projectCTP2ToolSection(section, strings.ToUpper)
	if err != nil {
		t.Fatal(err)
	}
	want := `[null,17,{"type":"namespace","name":"functions","tools":[null,false,{"type":"function","name":"keep","description":"ORIGINAL"}]}]`
	if !sameJSONValue(projected, []byte(want)) {
		t.Fatalf("tolerant projection lost malformed fields: %s", projected)
	}
}
