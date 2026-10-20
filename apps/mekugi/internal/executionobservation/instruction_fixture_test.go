package executionobservation

import (
	"os"

	"path/filepath"

	"testing"
)

func recordedInstructionCleanup(t *testing.T) (string, string) {
	t.Helper()
	read := func(name string) string {
		t.Helper()
		text, err := os.ReadFile(filepath.Join("testdata", "instruction-cleanup", name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(text)
	}
	// The removed plugin advertisement leaves its trailing line separator.
	return read("stock"), read("projected") + "\n"
}
