package responses

import "testing"

func TestSameJSONValueWireSemantics(t *testing.T) {
	for _, tt := range []struct {
		name, left, right string
		want              bool
	}{
		{"object order and escapes", `{"a":"<","b":[true,null]}`, ` {"b":[true,null],"a":"\u003c"} `, true},
		{"duplicate last wins", `{"a":0,"a":2}`, `{"a":2}`, true},
		{"nested duplicate last wins", `[{"a":0,"a":2}]`, `[{"a":2}]`, true},
		{"duplicate differs", `{"a":0,"a":2}`, `{"a":0}`, false},
		{"invalid UTF8 replacement", "\"\xff\"", `"\ufffd"`, true},
		{"unpaired surrogate replacement", `"\ud800"`, `"\ufffd"`, true},
		{"numeric whitespace", ` [9007199254740993,1.00,1e400] `, `[9007199254740993, 1.00, 1e400]`, true},
		{"numeric precision", `9007199254740992`, `9007199254740993`, false},
		{"decimal spelling", `{"n":1}`, `{"n":1.0}`, false},
		{"exponent spelling", `[1e3]`, `[1E3]`, false},
		{"signed zero", `0`, `-0`, false},
		{"array order", `[1,2]`, `[2,1]`, false},
		{"null versus empty", `null`, `[]`, false},
		{"trailing value", `{} {}`, `{}`, false},
		{"trailing garbage", `{}x`, `{}`, false},
		{"malformed", `{`, `{}`, false},
		// The exact-byte fast path also applies to values not yet validated.
		{"identical unvalidated bytes", `{`, `{`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, pair := range [][2]string{{tt.left, tt.right}, {tt.right, tt.left}} {
				if got := SameJSONValue([]byte(pair[0]), []byte(pair[1])); got != tt.want {
					t.Fatalf("SameJSONValue(%q, %q) = %v, want %v", pair[0], pair[1], got, tt.want)
				}
			}
		})
	}
}
