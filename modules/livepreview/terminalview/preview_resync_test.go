package terminalview

// Source: internal/ui/diffview/preview_resync_test.go@[a42ac9f1]
import (
	"testing"
	"testing/synctest"
	"time"
)

func TestPreviewRepeatedCompletionKeepsOriginalHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pane := PreviewPane{}
		preview := batchTestPreview("call", "/root", "source.go")
		pane.Update(preview)
		batchTestRender(t, &pane, 16)
		preview.Complete = true
		pane.Update(preview)
		time.Sleep(time.Second)
		pane.Update(preview)
		time.Sleep(600 * time.Millisecond)
		if !pane.ExpireBatches(time.Now(), 1500*time.Millisecond) || len(batchTestRender(t, &pane, 16)) != 0 {
			t.Fatal("replayed completion extended the original settling interval")
		}
	})
}
