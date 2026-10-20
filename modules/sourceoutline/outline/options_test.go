package outline

import "testing"

func TestReaderConsumerOptionBoundaries(t *testing.T) {
	path := nativeFixture(t, "source.go", "package source\nvar Value = 1\n")
	for _, name := range []string{"inspect_file"} {
		out := nativeExecute(t, path, "--max-tokens=15500")
		if out.ExitCode != 0 || out.Stdout == "" {
			t.Fatalf("%s inline budget: %+v", name, out)
		}
		out = nativeExecute(t, "--", "--max-tokens=0")
		if out.FailureClass != "not_found" {
			t.Fatalf("%s option-like path: %+v", name, out)
		}
	}
}
