package launch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/libraries/legacycall"

	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/libraries/diffmodel"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
)

func TestEditReceiptGroupsCaptureGapsWithoutClaimingEdits(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			history := legacycall.History{ChangeID: "amber1", ExecOutcome: (*legacycall.Outcome)(&changecapture.Outcome{Labels: []string{"python3"}})}
			if known {
				history.ReviewFiles = append(history.ReviewFiles, legacycall.ReviewFile(diffmodel.RenderReviewFile("edited.go", "edited.go", "old\n", "new\n")))
			}
			for i := range 100 {
				file := diffmodel.RenderIncompleteReviewFile(fmt.Sprintf("unknown-%d.go", i), fmt.Sprintf("unknown-%d.go", i), "capture limit")
				if i%2 == 0 {
					file.Origin = "generator"
				}
				history.ReviewFiles = append(history.ReviewFiles, legacycall.ReviewFile(file))
			}
			text := changepresentation.ReceiptText(t.TempDir(), receiptEvidence(history))
			blocks := parseLiveActivity(sessionpresentation.ActivityEntry{Kind: "tool", Text: text})
			wantBlocks := 1
			if known {
				wantBlocks++
				if blocks[0].Verb != "Edit" || !strings.Contains(text, "Edit `edited.go` +1 -1 · python3") {
					t.Fatalf("lost confirmed edit: %q", text)
				}
			}
			if len(blocks) != wantBlocks || blocks[len(blocks)-1].Verb != "Capture" ||
				!strings.Contains(text, "incomplete evidence for 50 paths (not confirmed edits): 50 × \"capture limit\"") || !strings.Contains(text, "mchanges amber1 --summary") ||
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
	history := legacycall.History{ChangeID: "amber1"}
	reasons := []string{"capture deadline", "capture deadline", "capture budget exhausted", "a read error\n\x1b[31m", strings.Repeat("b long error ", 100)}
	for i, reason := range reasons {
		path := fmt.Sprintf("candidate-%d.go", i)
		history.ReviewFiles = append(history.ReviewFiles, legacycall.ReviewFile(diffmodel.RenderIncompleteReviewFile(path, path, reason)))
	}
	text := changepresentation.ReceiptText("", receiptEvidence(history))
	blocks := parseLiveActivity(sessionpresentation.ActivityEntry{Kind: "tool", Text: text})
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
