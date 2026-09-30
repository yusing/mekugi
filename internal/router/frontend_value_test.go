package router

import "testing"

func TestParseMaxTokensCanonicalValue(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{
		{"1", 1},
		{"15500", 15500},
	} {
		got, err := parseMaxTokens(test.value)
		if err != nil || got != test.want {
			t.Errorf("parseMaxTokens(%q) = %d, %v; want %d, nil", test.value, got, err, test.want)
		}
	}
	for _, value := range []string{
		"", "0", "-1", "+1", "01", "1e3", " 1", "1 ", "\t1", "1\n", " ",
		"15501", "9999999999999999999999999999999999999999999999999999",
	} {
		t.Run(value, func(t *testing.T) {
			if got, err := parseMaxTokens(value); err == nil {
				t.Fatalf("parseMaxTokens(%q) = %d, nil; want rejection", value, got)
			}
		})
	}
}
