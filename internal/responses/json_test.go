package responses

import (
	"bytes"
	jsonv1 "encoding/json"
	"testing"
)

func TestProtocolJSONSpelling(t *testing.T) {
	for _, value := range []any{
		nil, []string(nil), map[string]string(nil),
		map[string]any{"z": "<>&\u2028\u2029", "a": "\xff", "n": jsonv1.Number("1.00")},
		struct {
			Empty bool `json:"empty,omitempty"`
			Bytes [2]byte
		}{Bytes: [2]byte{1, 2}},
		jsonv1.RawMessage(`{ "x":1,"x":2,"html":"<>&" }`),
		make(chan int), jsonv1.RawMessage(`{`),
	} {
		var previous bytes.Buffer
		encoder := jsonv1.NewEncoder(&previous)
		encoder.SetEscapeHTML(false)
		wantErr := encoder.Encode(value)
		got, err := MarshalJSON(value)
		if (err == nil) != (wantErr == nil) {
			t.Fatalf("%T: error=%v, previous=%v", value, err, wantErr)
		}
		if err == nil && !bytes.Equal(got, bytes.TrimSuffix(previous.Bytes(), []byte{'\n'})) {
			t.Fatalf("%T: got %s, previous %s", value, got, previous.Bytes())
		}
		func() {
			defer func() {
				if panicked := recover() != nil; panicked != (err != nil) {
					t.Fatalf("%T: panic=%v, error=%v", value, panicked, err)
				}
			}()
			if encoded := MustMarshalJSON(value); !bytes.Equal(encoded, got) {
				t.Fatalf("%T: must-encoding differs", value)
			}
		}()
	}
}

func TestStringFieldCompatibility(t *testing.T) {
	for _, raw := range []string{`"value"`, `null`, `1`, `{}`, `"\ud800"`, `"unfinished`, "\"\xff\"", ""} {
		var want string
		_ = jsonv1.Unmarshal([]byte(raw), &want)
		if got := StringField(map[string]jsonv1.RawMessage{"field": []byte(raw)}, "field"); got != want {
			t.Fatalf("%q: got %q, previous %q", raw, got, want)
		}
	}
}
