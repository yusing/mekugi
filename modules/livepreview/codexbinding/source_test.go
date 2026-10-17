package codexbinding

import (
	"context"
	"strconv"
	"testing"

	"github.com/yusing/mekugi/modules/livepreview/projection"
)

func TestFinalPreviewSourceHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	workspace := t.TempDir()
	for _, test := range []struct{ kind, input string }{
		{applyPatchToolName, "*** Begin Patch\n*** Add File: output.txt\n+content\n*** End Patch\n"},
		{nativeExecCommandToolName, `{"cmd":` + strconv.Quote("cat > output.txt <<'EOF'\ncontent\nEOF\n") + `}`},
	} {
		t.Run(test.kind, func(t *testing.T) {
			p, _ := Project(ctx, workspace, test.kind, test.input, true, projection.ReadFile)
			if len(p.Files) != 0 {
				t.Fatal("final projection escaped its supplied lifetime")
			}
		})
	}
}
