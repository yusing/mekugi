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
				!strings.Contains(text, "incomplete evidence for 100 paths (not confirmed edits): 100 × \"capture limit\"") || !strings.Contains(text, "mchanges amber1 --history") ||
				strings.Contains(text, "unknown-") || strings.Contains(text, "tool-managed files") {
				t.Fatalf("capture gaps became edit claims or flooded the receipt: %q", text)
			}
			if len(history.ReviewFiles) != 99+wantBlocks || history.ReviewFiles[len(history.ReviewFiles)-1].Incomplete != "capture limit" {
				t.Fatal("display discarded retained evidence")
			}
		})
	}
}

func TestEditReceiptCaptureReasonsAreBoundedAndRetained(t *testing.T) {
	history := mekugiHistory{ChangeID: "amber1"}
	reasons := []string{"capture deadline", "capture deadline", "capture budget exhausted", "a read error\n\x1b[31m", strings.Repeat("b long error ", 100)}
	for i, reason := range reasons {
		path := fmt.Sprintf("candidate-%d.go", i)
		history.ReviewFiles = append(history.ReviewFiles, mekugi.RenderIncompleteReviewFile(path, path, reason))
	}
	text := editReceiptText("", history)
	blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: text})
	if len(blocks) != 1 || blocks[0].Verb != "Capture" || !strings.Contains(text, `2 × "capture deadline"`) || !strings.Contains(text, "other reasons: 1") || len(text) > 650 || strings.ContainsAny(text, "\n\x1b") {
		t.Fatalf("unbounded or ambiguous capture receipt: %q", text)
	}
	for i, reason := range reasons {
		if history.ReviewFiles[i].Incomplete != reason {
			t.Fatal("receipt mutated retained reason")
		}
	}
	if !strings.Contains(text, "mchanges amber1 --summary") {
		t.Fatal("missing per-path detail command")
	}
}
