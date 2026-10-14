package toolplugin

import "testing"

func TestParseMaxTokensCanonicalValue(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{
		{"1", 1},
		{"15500", 15500},
	} {
		got, err := ParseMaxTokens(test.value)
		if err != nil || got != test.want {
			t.Errorf("ParseMaxTokens(%q) = %d, %v; want %d, nil", test.value, got, err, test.want)
		}
	}
	for _, value := range []string{
		"", "0", "-1", "+1", "01", "1e3", " 1", "1 ", "\t1", "1\n", " ",
		"15501", "9999999999999999999999999999999999999999999999999999",
	} {
		t.Run(value, func(t *testing.T) {
			if got, err := ParseMaxTokens(value); err == nil {
				t.Fatalf("ParseMaxTokens(%q) = %d, nil; want rejection", value, got)
			}
		})
	}
}

func TestReaderConsumerOptionBoundaries(t *testing.T) {
	path := nativeFixture(t, "source.go", "package source\nvar Value = 1\n")
	for _, name := range []string{"mcat", "inspect_file"} {
		out := nativeExecute(t, name, path, "--max-tokens=15500")
		if out.ExitCode != 0 || out.Stdout == "" {
			t.Fatalf("%s inline budget: %+v", name, out)
		}
		out = nativeExecute(t, name, "--", "--max-tokens=0")
		if out.FailureClass != "not_found" {
			t.Fatalf("%s option-like path: %+v", name, out)
		}
	}
	queries, workspace, explicit, budget, err := parseSymbolQueries([]string{"--workspace", "--max-tokens=0", "def", path, "Value", "--max-tokens=42"})
	if err != nil || len(queries) != 1 || workspace != "--max-tokens=0" || !explicit || budget != 42 {
		t.Fatalf("workspace value boundary: %v %q %v %d %v", queries, workspace, explicit, budget, err)
	}
}
