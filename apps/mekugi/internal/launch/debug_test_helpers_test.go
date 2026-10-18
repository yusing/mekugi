package launch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func diagnosticDebugOutput(t *testing.T) *requestexecution.Debug {
	t.Helper()
	d, err := openDebugOutput(testDiagnostics(new(evidence.Service), new(transportcapture.Service)), true, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close(); _ = os.RemoveAll(filepath.Dir(d.Paths()[0])) })
	return d
}
