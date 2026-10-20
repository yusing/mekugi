package capture

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestExecObservationBoundsEncodedCapture(t *testing.T) {
	workspace := t.TempDir()
	// JSON escapes control bytes as six bytes, so this content encodes past
	// the bound.
	writeTestFile(t, filepath.Join(workspace, "page.html"), strings.Repeat("\x01", 2<<20))
	writeTestFile(t, filepath.Join(workspace, "small"), "kept\n")
	// Exercise encoded-size admission independently of the forwarding deadline.
	// Race instrumentation can make this deliberately large fixture exceed it.
	observation, observed := captureExecObservationWithin([]commandanalysis.Command{{
		Command: "touch page.html small", Workdir: workspace, Shell: "bash",
	}}, false, false, Options{})
	if !observed || observation == nil || len(observation.Files) != 2 {
		t.Fatalf("named files were not captured: %+v", observation)
	}
	if size := len(responses.MustMarshalJSON(observation)); size > maxExecObservationBytes {
		t.Fatalf("encoded capture is %d bytes", size)
	}
	for _, file := range observation.Files {
		if strings.HasSuffix(file.Path, "page.html") && (file.Content != "" || file.Stamp == "") {
			t.Fatalf("large file kept its content: %d bytes", len(file.Content))
		}
		if strings.HasSuffix(file.Path, "small") && file.Content != "kept\n" {
			t.Fatalf("small file lost its content: %+v", file)
		}
	}
	if result := Reconcile(t.Context(), *observation, Comparison{}); !result.Complete || len(result.Files) != 0 {
		t.Fatalf("unchanged-file reconciliation: complete=%t reviews=%+v", result.Complete, result.Files)
	}
}
