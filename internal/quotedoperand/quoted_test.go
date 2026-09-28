package quotedoperand

import "testing"

func TestDecodeQuotedPreservesRemainder(t *testing.T) {
	for _, test := range []struct {
		source, value, rest string
	}{
		{`"a b" rest`, "a b", " rest"},
		{" \t\r\n\"a\tb\"\t tail", "a\tb", "\t tail"},
		{`"a\"b\\c" next`, "a\"b\\c", " next"},
		{`"世界\n\u0061"`, "世界\na", ""},
		{`"""next"`, "", `"next"`},
	} {
		t.Run(test.source, func(t *testing.T) {
			value, rest, err := DecodeQuoted(test.source)
			if err != nil || value != test.value || rest != test.rest {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, nil)", value, rest, err, test.value, test.rest)
			}
		})
	}
}

func TestDecodeQuotedRejectsMalformedOperands(t *testing.T) {
	for _, source := range []string{"", " \t", "unquoted", `'single'`, `"unfinished`, `"bad\q"`, "\"line\nbreak\"", "\"control\x00\""} {
		t.Run(source, func(t *testing.T) {
			value, rest, err := DecodeQuoted(source)
			if err == nil || value != "" || rest != "" {
				t.Fatalf("malformed operand returned (%q, %q, %v)", value, rest, err)
			}
		})
	}
}
