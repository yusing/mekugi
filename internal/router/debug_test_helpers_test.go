package router

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func diagnosticDebugOutput(t *testing.T) *debugOutput {
	t.Helper()
	flags := newRouterFlags(io.Discard)
	flags.debug = true
	d, err := openDebugOutput(testHistory(), flags.debug, flags.captureOutput)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.close(); _ = os.RemoveAll(filepath.Dir(d.paths[0])) })
	return d
}
