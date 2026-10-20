package responses_test

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/backends/codex/responses"
)

func TestRequestCatalogSectionStates(t *testing.T) {
	for _, tc := range []struct {
		name, body          string
		present, array, err bool
	}{
		{"absent", `{}`, false, false, false},
		{"null", `{"tools":null}`, true, false, false},
		{"malformed", `{"tools":{}}`, true, false, true},
		{"array", `{"tools":[]}`, true, true, false},
		{"malformed member", `{"tools":[42]}`, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := parseCatalogRequest(t, tc.body)
			section := responses.DecodeToolCatalog(request.Fields).Top
			if section.Present != tc.present || section.Array != tc.array || (section.Err != nil) != tc.err {
				t.Fatalf("section = %+v", section)
			}
			// Admission policy can inspect the catalog without rewriting opaque input.
			assertCatalogWire(t, request, tc.body)
		})
	}
}

func TestRequestCatalogPreservesExtensionsDuringEncoding(t *testing.T) {
	request := parseCatalogRequest(t, `{"tools":[{"type":"function","name":"top","description":"old","provider":{"opaque":[1,true]}}],"input":[{"type":"additional_tools","group_extension":{"keep":true},"tools":[{"type":"namespace","name":"space","namespace_extension":"keep","tools":[{"type":"function","name":"nested","description":"old","parameters":{"type":"object"},"plugin_extension":["keep"]}]}]}]}`)
	catalog := responses.DecodeToolCatalog(request.Fields)
	catalog.Top.Tools[0].SetDescription("new top")
	if err := catalog.EncodeTop(request.Fields); err != nil {
		t.Fatal(err)
	}
	group := catalog.Additional[0]
	nested := group.Tools.Tools[0].Nested
	nested.Tools[0].SetDescription("new nested")
	if err := catalog.EncodeAdditional(request.Fields, group, nested); err != nil {
		t.Fatal(err)
	}
	assertCatalogWire(t, request, `{"tools":[{"type":"function","name":"top","description":"new top","provider":{"opaque":[1,true]}}],"input":[{"type":"additional_tools","group_extension":{"keep":true},"tools":[{"type":"namespace","name":"space","namespace_extension":"keep","tools":[{"type":"function","name":"nested","description":"new nested","parameters":{"type":"object"},"plugin_extension":["keep"]}]}]}]}`)
}

func TestRequestCatalogRawFieldsAreAuthoritative(t *testing.T) {
	request := parseCatalogRequest(t, `{"tools":[{"type":"function","name":"old","description":"old"}]}`)
	catalog := responses.DecodeToolCatalog(request.Fields)
	tool := catalog.Top.Tools[0]
	tool.SetRawField("type", jsontext.Value(`"custom"`))
	tool.SetRawField("name", jsontext.Value(`"new"`))
	tool.SetRawField("description", jsontext.Value(`"raw description"`))
	if tool.Type() != "custom" || tool.Name() != "new" || tool.Description() != "raw description" {
		t.Fatalf("stale getters: type=%q name=%q description=%q", tool.Type(), tool.Name(), tool.Description())
	}
	tool.SetDescription("final description")
	if tool.Description() != "final description" || string(tool.RawField("description")) != `"final description"` {
		t.Fatal("description setter and raw field diverged")
	}
	if err := catalog.EncodeTop(request.Fields); err != nil {
		t.Fatal(err)
	}
	assertCatalogWire(t, request, `{"tools":[{"type":"custom","name":"new","description":"final description"}]}`)
}

func parseCatalogRequest(t *testing.T, body string) responses.Request {
	t.Helper()
	request, err := responses.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func assertCatalogWire(t *testing.T, request responses.Request, want string) {
	t.Helper()
	wire, err := request.WireBody(request.Fields)
	if err != nil {
		t.Fatal(err)
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(wire, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("wire = %s\nwant = %s", wire, want)
	}
}
