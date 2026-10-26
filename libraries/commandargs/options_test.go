package commandargs

import "testing"

func TestParseMaxTokensCanonicalValue(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{
		{"1", 1},
		{"15500", 15500},
		{"15501", MaxOutputTokens},
		{"9999999999999999999999999999999999999999999999999999", MaxOutputTokens},
	} {
		got, err := ParseMaxTokens(test.value)
		if err != nil || got != test.want {
			t.Errorf("ParseMaxTokens(%q) = %d, %v; want %d, nil", test.value, got, err, test.want)
		}
	}
	for _, value := range []string{
		"", "0", "-1", "+1", "01", "1e3", " 1", "1 ", "\t1", "1\n", " ",
		"015501", "9999999999999999999999999999999999999999999999999999x",
	} {
		t.Run(value, func(t *testing.T) {
			if got, err := ParseMaxTokens(value); err == nil || err.Error() != MaxTokensArgumentError {
				t.Fatalf("ParseMaxTokens(%q) = %d, %v; want %s", value, got, err, MaxTokensArgumentError)
			}
		})
	}
}

func TestReaderMaxTokensError(t *testing.T) {
	for _, args := range [][]string{{"--max-tokens", "0"}, {"--max-tokens", "2", "--max-tokens", "3"}} {
		if _, _, err := ParseReaderOptions(args, true, 100, ""); err == nil || err.Error() != MaxTokensArgumentError {
			t.Fatalf("ParseReaderOptions(%q) error = %v; want %s", args, err, MaxTokensArgumentError)
		}
	}
}
