package activity

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotReasoningRosterWidth(t *testing.T) {
	p := Painter{Theme: livediff.DarkTheme}
	for _, width := range []int{20, 80} {
		for _, live := range []bool{true, false} {
			t.Run(fmt.Sprintf("%d-live-%v", width, live), func(t *testing.T) {
				block := Block{Kind: "summary", Body: "Checking the implementation.", Live: live}
				uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/reasoning_roster_width_%d_live_%v.txt", width, live), p.Summary([]Block{block}, width)+"\n")
			})
		}
	}
}

func TestUISnapshotReasoningRosterTitles(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		live       bool
	}{
		{"bold", "**Inspecting the renderer**\n\n" + reasoningSnapshotBody, true},
		{"markdown", "## Checking C# ##\n\n" + reasoningSnapshotBody, true},
		{"plain", "Checking timing\n\n" + reasoningSnapshotBody, true},
		{"fenced", "**Real title**\n\n```markdown\n# Not a title\n**Also code**\n```\n\nMore detail.", true},
		{"sections", "**Inspecting**\n\nFirst paragraph.\n\n**Validating**\n\nSecond paragraph.", true},
		{"untitled-live", reasoningSnapshotBody, true},
		{"untitled-done", reasoningSnapshotBody, false},
		{"paragraph-live", strings.Repeat("Checking the implementation. ", 100), true},
		{"paragraph-done", strings.Repeat("Checking the implementation. ", 100), false},
		{"short", "Checking the output.", true},
		{"heading-only", "**Checking tests**\n<!-- -->", true},
		{"pending", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Painter{Theme: livediff.DarkTheme}
			blocks := ReasoningSections(tc.body)
			for i := range blocks {
				blocks[i].Live, blocks[i].Elapsed = tc.live, "12s"
			}
			before := slices.Clone(blocks)
			uisnapshot.Assert(t, "testdata/snapshots/reasoning_roster_"+tc.name+".txt", p.Summary(blocks, 80)+"\n")
			if !reflect.DeepEqual(blocks, before) {
				t.Fatal("roster formatting mutated transcript blocks")
			}
			for _, block := range blocks {
				if page := p.DialogPage(block, 80); page.Text != block.Body {
					t.Fatalf("roster formatting changed dialog content: %q", page.Text)
				}
			}
		})
	}
}

func TestReasoningRosterUsesExplicitLabel(t *testing.T) {
	p := Painter{Theme: livediff.DarkTheme}
	block := Block{Kind: "summary", Label: "Checking `renderer`", Body: "**Other heading**\n\n" + reasoningSnapshotBody, Live: true}
	if got := ansi.Strip(p.Summary([]Block{block}, 80)); got != "Checking renderer" {
		t.Fatalf("explicit transcript label ignored: %q", got)
	}
}

func TestReasoningRosterConsecutiveSummaries(t *testing.T) {
	p := Painter{Theme: livediff.DarkTheme}
	blocks := []Block{
		{Kind: "op", Verb: "Read", Label: "file.go"},
		{Kind: "summary", Body: "**Inspecting**\n\n" + reasoningSnapshotBody},
		{Kind: "summary", Body: "**Validating**\n\n" + reasoningSnapshotBody},
		{Kind: "filter"},
	}
	if got := ansi.Strip(p.Summary(blocks, 80)); got != "Inspecting, Validating" {
		t.Fatalf("consecutive titles lost order or included body: %q", got)
	}
	blocks = append(blocks, Block{Kind: "op", Verb: "Read", Label: "next.go"})
	if got := ansi.Strip(p.Summary(blocks, 80)); !strings.HasPrefix(got, "Read next.go") {
		t.Fatalf("reasoning replaced a newer operation: %q", got)
	}
}
