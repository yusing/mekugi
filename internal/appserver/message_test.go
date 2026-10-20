package appserver_test

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"

	"github.com/yusing/mekugi/internal/appserver"
)

func TestAppServerMessageKeepsEmptyObjectParams(t *testing.T) {
	for _, params := range []jsontext.Value{nil, []byte(`{}`), []byte(`{"includeHidden":true}`)} {
		wire, err := json.Marshal(&appserver.Message{Method: "model/list", Params: params})
		if err != nil {
			t.Fatal(err)
		}
		var message map[string]jsontext.Value
		if err := json.Unmarshal(wire, &message); err != nil {
			t.Fatal(err)
		}
		if string(message["params"]) != string(params) {
			t.Fatalf("params=%s wire=%s", params, wire)
		}
	}
}
