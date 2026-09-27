package router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestEditReceiptGroupsCaptureGapsWithoutClaimingEdits(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			history := mekugiHistory{ChangeID: "amber1", ExecOutcome: &execOutcome{Labels: []string{"python3"}}}
			if known {
				history.ReviewFiles = append(history.ReviewFiles, mekugi.RenderReviewFile("edited.go", "edited.go", "old\n", "new\n"))
			}
			for i := range 100 {
				file := mekugi.RenderIncompleteReviewFile(fmt.Sprintf("unknown-%d.go", i), fmt.Sprintf("unknown-%d.go", i), "capture limit")
				if i%2 == 0 {
					file.Origin = "generator"
				}
				history.ReviewFiles = append(history.ReviewFiles, file)
			}
			text := editReceiptText(t.TempDir(), history)
			blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: text})
			wantBlocks := 1
			if known {
				wantBlocks++
				if blocks[0].Verb != "Edit" || !strings.Contains(text, "Edit `edited.go` +1 -1 · python3") {
					t.Fatalf("lost confirmed edit: %q", text)
				}
			}
			if len(blocks) != wantBlocks || blocks[len(blocks)-1].Verb != "Capture" ||
				!strings.Contains(text, "evidence unavailable for 100 paths") || !strings.Contains(text, "mchanges amber1 --summary") ||
				strings.Contains(text, "unknown-") || strings.Contains(text, "tool-managed files") {
				t.Fatalf("capture gaps became edit claims or flooded the receipt: %q", text)
			}
			if len(history.ReviewFiles) != 99+wantBlocks || history.ReviewFiles[len(history.ReviewFiles)-1].Incomplete != "capture limit" {
				t.Fatal("display discarded retained evidence")
			}
		})
	}
}
