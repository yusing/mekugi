package responses

import (
	"bytes"
	"encoding/json/jsontext"
	"strings"
	"testing"

	protocol "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestInputPrefixRetainsOnlyInstructions(t *testing.T) {
	var prefix InputPrefix
	prepare := func(native, projected string) Request {
		t.Helper()
		request, err := ParseRequest([]byte(`{"model":"model","input":` + native + `}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Fields["input"] = jsontext.Value(projected)
		if err := prefix.Freeze(&request); err != nil {
			t.Fatal(err)
		}
		return request
	}
	native := `[{"role":"developer","content":"native permission"},{"role":"user","content":"task"}]`
	first := prepare(native, `[{"role":"developer","content":"frozen"},{"role":"user","content":"task"}]`)
	suffix := `{"role":"user","content":"task"}, {"type":"function_call","name":"tool","call_id":"call","arguments":"{}"}, {"type":"function_call_output","call_id":"call","output":"` + strings.Repeat("result", 10000) + `"}]`
	next := prepare(native, `[{"role":"developer","content":"changed"},`+suffix)
	if !bytes.HasSuffix(next.Fields["input"], []byte(suffix)) || bytes.Contains(next.Fields["input"], []byte("changed")) || !bytes.Contains(next.Fields["input"], []byte("frozen")) {
		t.Fatal("prefix freeze changed the native conversation/tool suffix")
	}
	if len(prefix.frozen.items) != 1 || len(prefix.frozen.items[0]) > 100 || len(first.Fields["input"]) > 200 {
		t.Fatal("prefix retained growing conversation history")
	}
	changed := prepare(strings.ReplaceAll(native, "native permission", "new permission"), `[{"role":"developer","content":"new permission"},{"role":"user","content":"task"}]`)
	if !bytes.Contains(changed.Fields["input"], []byte("new permission")) {
		t.Fatal("frozen prefix overrode native authority")
	}
}

func TestInputPrefixAbsentInstructionsStayAbsent(t *testing.T) {
	var prefix InputPrefix
	for i := range 2 {
		request, err := ParseRequest([]byte(`{"model":"model","input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if i != 0 {
			request.Fields["instructions"] = protocol.MustMarshalJSON("later contribution")
		}
		if err := prefix.Freeze(&request); err != nil {
			t.Fatal(err)
		}
		if _, exists := request.Fields["instructions"]; exists {
			t.Fatal("later contribution rewrote the frozen empty base")
		}
	}
}
