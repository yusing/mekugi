package toolplugin

import (
	"testing"
)

func TestNativeMCatTrailingOptionTerminator(t *testing.T) {
	file := nativeFixture(t, "fixture.txt", "read\n")
	result := nativeExecute(t, "mcat", file, "--")
	if result.ExitCode != 0 || result.Stdout != "read\n" {
		t.Fatalf("trailing terminator: %+v", result)
	}
}
