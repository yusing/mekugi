package router

import (
	"os"
	"path/filepath"
	"testing"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

func diagnosticDebugOutput(t *testing.T) *debugOutput {
	t.Helper()
	d, err := openDebugOutput(testDiagnostics(new(evidence.Service), new(transportcapture.Service)), true, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close(); _ = os.RemoveAll(filepath.Dir(d.Paths()[0])) })
	return d
}
