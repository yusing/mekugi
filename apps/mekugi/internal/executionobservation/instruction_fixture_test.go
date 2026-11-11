package executionobservation

import (
	"os"

	"path/filepath"

	"testing"
)

func nativeInstructionFixture(t *testing.T) (string, string) {
	t.Helper()
	read := func(name string) string {
		t.Helper()
		text, err := os.ReadFile(filepath.Join("testdata", "instruction-cleanup", name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(text)
	}
	// Keep the excerpt boundary separate from its policy text.
	return read("native"), read("projected") + "\n"
}
