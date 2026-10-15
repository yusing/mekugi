package query

import "testing"

func TestWorkspaceOptionBoundary(t *testing.T) {
	path := nativeFixture(t, "source.go", "package source\nvar Value = 1\n")
	queries, workspace, explicit, budget, err := parseSymbolQueries([]string{"--workspace", "--max-tokens=0", "def", path, "Value", "--max-tokens=42"})
	if err != nil || len(queries) != 1 || workspace != "--max-tokens=0" || !explicit || budget != 42 {
		t.Fatalf("workspace value boundary: %v %q %v %d %v", queries, workspace, explicit, budget, err)
	}
}
