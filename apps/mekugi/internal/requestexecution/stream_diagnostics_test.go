package requestexecution

import (
	"context"
	"io"
	"testing"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestStreamDiagnosticsPreserveEndOrigin(t *testing.T) {
	d := &transportcapture.Stream{}
	recordStreamReadEnd(d, io.EOF, "downstream")
	recordStreamReadEnd(d, context.Canceled, "context")
	recordStreamReadEnd(d, io.EOF, "")
	recordStreamCopyStop(d, nil)
	if d.ReadOrigin != "downstream" || d.ReadTermination != "downstream_eof" || d.EndReason != "downstream_eof" {
		t.Fatalf("first observed cause was overwritten: %+v", d)
	}
}
