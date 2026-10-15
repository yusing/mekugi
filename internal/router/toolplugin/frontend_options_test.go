package toolplugin

import "testing"

func TestReaderConsumerOptionBoundaries(t *testing.T) {
	path := nativeFixture(t, "source.go", "package source\nvar Value = 1\n")
	for _, name := range []string{"mcat"} {
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
