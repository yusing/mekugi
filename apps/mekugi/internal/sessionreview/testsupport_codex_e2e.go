//go:build codex_e2e

package sessionreview

import (
	"fmt"
	"testing"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// TestPreviewOverflow exercises the real mailbox while its native consumer is
// paused. This host-test fixture is excluded from production builds.
func (r *Review) TestPreviewOverflow(t testing.TB) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	sub := r.subscriber
	if sub == nil || r.preview == nil {
		t.Fatal("native UI preview subscriber missing")
	}
	for n := range 33 {
		r.preview.Update(previewmodel.Preview{ID: fmt.Sprintf("withdrawn-%d", n)}, true)
	}
	select {
	case <-sub.PreviewGap():
	default:
		t.Fatal("preview mailbox overflow did not signal a gap")
	}
}
