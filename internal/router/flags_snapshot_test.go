package router

import (
	"bytes"
	"testing"

	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotCLIHelp(t *testing.T) {
	var output bytes.Buffer
	PrintUsage(&output)
	uisnapshot.Assert(t, "testdata/snapshots/cli-help.txt", output.String())
}
