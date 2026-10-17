package router

import (
	"testing"

	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func waitExecScopePreview(t *testing.T, broker *previewmodel.Session, match func(previewmodel.Preview) bool) previewmodel.Preview {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, preview := range broker.Current() {
			if match(preview) {
				return preview
			}
		}
		select {
		case <-timer.C:
			t.Fatal("timed out waiting for exec scope preview")
		case <-ticker.C:
		}
	}
}
