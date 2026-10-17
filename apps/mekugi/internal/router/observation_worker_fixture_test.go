package router

import (
	"testing"

	"time"

	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"
)

func waitLiveDiffWorkerDone(t *testing.T, worker *previewbinding.Stream) {
	t.Helper()
	select {
	case <-worker.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("live diff preview worker did not stop")
	}
}
