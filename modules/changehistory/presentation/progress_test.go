package presentation

import (
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

func TestProgressExposesTypedLegacyIntervalWithoutParsingLabels(t *testing.T) {
	chunk := Chunk{Key: "/workspace\x00call/0", Workspace: "/workspace", Origin: Origin{Caller: "/root/worker", Change: "amber1"},
		Review: diffmodel.RenderReviewFile("old", "new", "before\n", "after\n")}
	publication := Publication{Files: []File{{Chunks: []Chunk{chunk}}}}
	count := 0
	for capture := range publication.Progress(map[string]bool{"/root/worker": true}, nil) {
		count++
		if capture.RecordOrder != 0 || capture.LegacyRecordKey == nil || *capture.LegacyRecordKey != chunk.Key || capture.FirstChunk != 1 || capture.Chunks != 1 {
			t.Fatalf("legacy interval=%+v", capture)
		}
		for repeated := range publication.Progress(map[string]bool{"/root/worker": true}, map[string]int{capture.Key: 1}) {
			t.Fatalf("acknowledged legacy interval repeated: %+v", repeated)
		}
	}
	if count != 1 {
		t.Fatalf("legacy capture count=%d", count)
	}
}
