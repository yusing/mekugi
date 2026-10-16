package commandargs

import (
	"reflect"
	"testing"
)

func TestWorkspaceOptionBoundary(t *testing.T) {
	path := "source.go"
	arguments, err := ParseSymbolQueries([]string{"--workspace", "--max-tokens=0", "def", path, "Value", "--max-tokens=42"})
	if err != nil || len(arguments.Queries) != 1 || arguments.Workspace != "--max-tokens=0" || !arguments.ExplicitWorkspace || arguments.Budget != 42 {
		t.Fatalf("workspace value boundary: %+v %v", arguments, err)
	}
}

func TestSymbolQueriesPreserveSelectorsAndOrder(t *testing.T) {
	arguments, err := ParseSymbolQueries([]string{
		"--max-tokens=72", "def", `"file with space.go":12`, "pkg.Target", "2",
		"refs", "other.go", "Other", "--workspace", "/selected",
	})
	want := SymbolArguments{
		Queries: []SymbolQuery{
			{Mode: "def", Path: "file with space.go", Name: "Target", Line: 12, Occurrence: 2},
			{Mode: "refs", Path: "other.go", Name: "Other"},
		},
		Workspace: "/selected", ExplicitWorkspace: true, Budget: 72,
	}
	if err != nil || !reflect.DeepEqual(arguments, want) {
		t.Fatalf("arguments = %+v, %v; want %+v", arguments, err, want)
	}
}
