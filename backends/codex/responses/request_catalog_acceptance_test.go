package responses_test

import (
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
