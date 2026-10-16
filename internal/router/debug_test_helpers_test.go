package router

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

func diagnosticDebugOutput(t *testing.T) *debugOutput {
	t.Helper()
	flags := newRouterFlags(io.Discard)
	flags.debug = true
	d, err := openDebugOutput(testHistory(), new(evidence.Service), new(transportcapture.Service), flags.debug, flags.captureOutput)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.close(); _ = os.RemoveAll(filepath.Dir(d.paths[0])) })
	return d
}
