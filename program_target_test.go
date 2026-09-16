package mekugi

import "testing"

func TestParseFailureTargetVariantComesFromTargetParser(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		source string
		want   targetVariant
	}{
		{name: "line", source: `type 1:aaaa "unterminated`, want: targetVariantLine},
		{name: "range", source: `type 1:aaaa..bad "value"`, want: targetVariantRange},
		{name: "single text", source: `type 1:aaaa "target" "value" trailing`, want: targetVariantTextSingle},
		{name: "multiple text", source: `type 1:aaaa "target" nope "value"`, want: targetVariantTextMultiple},
		{name: "unanchored multiple text", source: `type "target" nope`, want: targetVariantTextMultiple},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parse(test.source)
			failures := commandsOf(err)
			if len(failures) != 1 || failures[0].Target != test.want {
				t.Fatalf("parse(%q) failures = %+v, want target %v", test.source, failures, test.want)
			}
		})
	}
}
