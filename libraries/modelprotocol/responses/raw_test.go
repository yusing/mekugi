package responses_test

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestRawItemEditsPreserveProviderFieldsInInput(t *testing.T) {
	input, err := responses.DecodeInput([]byte(`[
		{"type":"message","role":"developer","phase":"commentary","content":[{"type":"input_text","text":"old","annotation":{"score":1.00}}],"provider":{"token":9007199254740993}},
		{"type":"custom_tool_call","call_id":"call-1","input":"old","arguments":null,"provider":{"signature":"<opaque>"}},
		{"type":"future_item","payload":{"number":1e20}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	message, ok := responses.DecodeItem(input.Items[0])
	if !ok {
		t.Fatal("message was not decoded")
	}
	if facts := responses.Facts(message.Fields); facts.Kind != responses.Message || facts.Role != "developer" || facts.Phase != "commentary" {
		t.Fatalf("message facts: %+v", facts)
	}
	var parts []jsonv1.RawMessage
	if err := json.Unmarshal(message.Content, &parts); err != nil {
		t.Fatal(err)
	}
	parts[0], err = responses.ReplaceField(parts[0], "text", responses.MustMarshalJSON("rewritten"))
	if err != nil {
		t.Fatal(err)
	}
	message.SetContent(responses.MustMarshalJSON(parts))
	input.Items[0] = responses.MustMarshalJSON(message)
	call, ok := responses.DecodeItem(input.Items[1])
	if !ok || call.CallID != "call-1" || call.Arguments == nil || *call.Arguments != "" {
		t.Fatalf("tool call: %+v, decoded=%v", call, ok)
	}
	call.SetInput("retained carrier")
	input.Items[1] = responses.MustMarshalJSON(call)
	encoded, err := input.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"content":[{"annotation":{"score":1.00},"text":"rewritten","type":"input_text"}],"phase":"commentary","provider":{"token":9007199254740993},"role":"developer","type":"message"},{"arguments":null,"call_id":"call-1","input":"retained carrier","provider":{"signature":"<opaque>"},"type":"custom_tool_call"},{"type":"future_item","payload":{"number":1e20}}]`
	if string(encoded) != want {
		t.Fatalf("encoded input:\n%s\nwant:\n%s", encoded, want)
	}
	if string(message.Content) != string(message.Fields["content"]) || call.Input == nil || *call.Input != "retained carrier" {
		t.Fatal("edited item views disagree with serialized fields")
	}
}

func TestRawInputKnownShapes(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
		text bool
	}{
		{"text", ` "hello" `, `"hello"`, true},
		{"legacy null text", ` null `, `""`, true},
		{"array", ` [ {"opaque":1.00}, false ] `, `[{"opaque":1.00},false]`, false},
		{"empty array", ` [] `, `[]`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, err := responses.DecodeInput([]byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if (input.Text != nil) != test.text {
				t.Fatalf("text view: %v", input.Text)
			}
			encoded, err := input.Encode()
			if err != nil || string(encoded) != test.want {
				t.Fatalf("encoded=%s, error=%v; want %s", encoded, err, test.want)
			}
			if test.text {
				*input.Text = "edited"
				encoded, err = input.Encode()
				if err != nil || string(encoded) != `"edited"` {
					t.Fatalf("edited text=%s, error=%v", encoded, err)
				}
			}
		})
	}
}

func TestRawInputOpaqueBytes(t *testing.T) {
	for _, raw := range []string{` { "future" : [1.00, 9007199254740993] } `, ` 1e20 `, ` true `, ` 1e1000000 `, ` {"x":1,"x":2} `, " {\"future\":\"\xff\"} "} {
		t.Run(raw, func(t *testing.T) {
			source := []byte(raw)
			input, err := responses.DecodeInput(source)
			if err != nil {
				t.Fatal(err)
			}
			if input.Text != nil || input.Items != nil {
				t.Fatal("opaque input acquired a known shape")
			}
			source[0] = '\n'
			encoded, err := input.Encode()
			if err != nil || !bytes.Equal(encoded, []byte(raw)) {
				t.Fatalf("opaque input=%q, error=%v; want %q", encoded, err, raw)
			}
			encoded[0] = '\t'
			again, err := input.Encode()
			if err != nil || string(again) != raw {
				t.Fatalf("encoded bytes alias retained input: %q, error=%v", again, err)
			}
		})
	}
}

func TestRawDecodeCompatibility(t *testing.T) {
	for _, raw := range []string{`"text"`, `null`, `[]`, `{}`, `true`, `1`, `"unfinished`, ``} {
		text, stringOK := responses.DecodeString([]byte(raw))
		wantString := raw == `"text"` || raw == `null`
		if stringOK != wantString || raw == `null` && text != "" || raw == `"text"` && text != "text" {
			t.Fatalf("DecodeString(%q)=%q, %v", raw, text, stringOK)
		}
		_, itemOK := responses.DecodeItem([]byte(raw))
		if itemOK != (raw == `{}`) {
			t.Fatalf("DecodeItem(%q) decoded=%v", raw, itemOK)
		}
		if _, err := responses.ReplaceField([]byte(raw), "content", jsonv1.RawMessage(`"edited"`)); (err == nil) != itemOK {
			t.Fatalf("ReplaceField(%q) error=%v", raw, err)
		}
		_, err := responses.DecodeInput([]byte(raw))
		wantError := raw == `"unfinished` || raw == ``
		if (err != nil) != wantError {
			t.Fatalf("DecodeInput(%q) error=%v", raw, err)
		}
	}
}

func TestRawInputRejectsTrailingValues(t *testing.T) {
	for _, raw := range []string{`true false`, `{} garbage`, `1 2`, `[] []`, `"text" {}`, `null true`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := responses.DecodeInput([]byte(raw)); err == nil {
				t.Fatal("accepted input after the first JSON value")
			}
		})
	}
}
