package activity_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestGroupOperationsExplorationAcrossEntries(t *testing.T) {
	input := []activityui.Block{
		{Source: 1, Kind: "reads", Verb: "Read", Reads: []activityui.Read{{Path: "a.go"}}},
		{Source: 2, Kind: "op", Verb: "Search", Label: "needle", Results: new(3)},
		{Source: 3, Kind: "op", Verb: "List", Label: "internal"},
		{Source: 4, Kind: "op", Verb: "Inspect", Label: "a.go"},
		{Source: 5, Kind: "op", Verb: "Run", Code: "go test ./..."},
		{Source: 6, Kind: "op", Verb: "Read", Label: "b.go"},
	}
	original := append([]activityui.Block(nil), input...)
	got := activityui.GroupOperations(input)
	if !reflect.DeepEqual(input, original) {
		t.Fatal("grouping mutated the input activity entries")
	}
	if len(got) != len(input) {
		t.Fatalf("got %d blocks, want %d", len(got), len(input))
	}
	for i, block := range got {
		heading := "Explored"
		if i == 4 {
			heading = ""
		}
		if block.GroupHeader != heading || block.GroupStart != (i == 0 || i == 5) {
			t.Errorf("block %d group = %q, start=%t", i, block.GroupHeader, block.GroupStart)
		}
		block.GroupHeader, block.GroupStart = "", false
		if !reflect.DeepEqual(block, input[i]) {
			t.Errorf("grouping changed operation %d: %#v", i, block)
		}
	}
	p := activityui.Painter{}
	var rows []string
	for _, block := range got {
		rows = append(rows, p.Block(block, 100)...)
	}
	if rendered := ansi.Strip(strings.Join(rows, "\n")); strings.Count(rendered, "• Explored") != 2 {
		t.Fatalf("exploration headings did not respect Run boundary:\n%s", rendered)
	} else if !strings.Contains(rendered, "needle (3 results)") {
		t.Fatalf("grouping lost search result count:\n%s", rendered)
	}
}

func TestExploredGroupUsesUniformIndentation(t *testing.T) {
	p := activityui.Painter{}
	blocks := activityui.GroupOperations([]activityui.Block{
		{Kind: "reads", Verb: "Read", Reads: []activityui.Read{{Path: "a.go"}}},
		{Kind: "op", Verb: "Search", Label: "`needle`"},
		{Kind: "op", Verb: "Inspect", Label: "`b.go`"},
	})
	for i, block := range blocks {
		rows := p.Block(block, 80)
		if i == 0 {
			if rows[0] != "• Explored" {
				t.Fatalf("unexpected exploration heading: %q", rows[0])
			}
			rows = rows[1:]
		}
		for _, row := range rows {
			plain := ansi.Strip(row)
			if !strings.HasPrefix(plain, "    "+block.Verb+" ") {
				t.Errorf("exploration row is not uniformly indented: %q", plain)
			}
		}
	}
}

func TestGroupOperationsEditInvocationHeaders(t *testing.T) {
	for _, source := range []string{"python3", "apply_patch", "perl"} {
		t.Run(source, func(t *testing.T) {
			input := []activityui.Block{
				{Source: 1, Kind: "op", Verb: "Edit", Label: "`a.go` +3 -1 · " + source, EditSource: source, EditHeader: true},
				{Source: 1, Kind: "op", Verb: "Create", Label: "`b.go` +2 -0 · " + source, EditSource: source},
				{Source: 2, Kind: "op", Verb: "Delete", Label: "`c.go` +0 -4 · " + source, EditSource: source, EditHeader: true, ExitCode: 2},
			}
			p := activityui.Painter{}
			var rows []string
			for i, block := range activityui.GroupOperations(input) {
				if block.GroupHeader != source || block.GroupStart != (i != 1) {
					t.Fatalf("block %d lost invocation boundary: %#v", i, block)
				}
				rows = append(rows, p.Block(block, 100)...)
			}
			for _, row := range rows {
				plain := ansi.Strip(row)
				if !strings.HasPrefix(plain, "• ") && !strings.HasPrefix(plain, "    Edit ") && !strings.HasPrefix(plain, "    Create ") && !strings.HasPrefix(plain, "    Delete ") {
					t.Errorf("file row is not uniformly indented: %q", plain)
				}
			}
			rendered := ansi.Strip(strings.Join(rows, "\n"))
			if strings.Count(rendered, "• "+source) != 2 || strings.Count(rendered, source) != 2 {
				t.Fatalf("source must appear only in each invocation header:\n%s", rendered)
			}
			for _, want := range []string{"a.go +3 -1", "b.go +2 -0", "c.go +0 -4", "(exit 2)"} {
				if !strings.Contains(rendered, want) {
					t.Errorf("missing %q in grouped edits:\n%s", want, rendered)
				}
			}
		})
	}
}

func TestEditGroupLongCommandHeaderWrapping(t *testing.T) {
	p := activityui.Painter{}
	source := "git stash push -m 'save pending changes before rebase'"
	for _, width := range []int{24, 40} {
		block := activityui.GroupOperations([]activityui.Block{
			{Kind: "op", Verb: "Edit", Label: "`a.go` +2 -1 · " + source, EditSource: source, EditHeader: true},
		})[0]
		rows := p.Block(block, width)
		for _, row := range rows {
			if ansi.StringWidth(row) > width {
				t.Fatalf("header or file row exceeds width %d: %q", width, row)
			}
		}
		plain := strings.Join(strings.Fields(ansi.Strip(strings.Join(rows, " "))), " ")
		if !strings.Contains(plain, "• "+source) || !strings.Contains(plain, "Edit a.go +2 -1") {
			t.Fatalf("wrapped group lost content at width %d: %q", width, plain)
		}
	}
}

func TestOperationGroupWrapping(t *testing.T) {
	for _, width := range []int{24, 40} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			p := activityui.Painter{}
			blocks := activityui.GroupOperations([]activityui.Block{
				{Kind: "op", Verb: "Edit", Label: "`internal/router/long_activity_filename.go` +13 -11 · python3", EditSource: "python3", EditHeader: true},
				{Kind: "reads", Verb: "Read", Reads: []activityui.Read{{Path: "internal/router/long_activity_filename.go", Ranges: []string{"1:100"}}}},
			})
			for _, block := range blocks {
				rows := p.Block(block, width)
				if len(rows) < 3 {
					t.Fatalf("expected wrapped nested operation, got %q", rows)
				}
				for i, row := range rows {
					if ansi.StringWidth(row) > width {
						t.Errorf("row %d exceeds width %d: %q", i, width, ansi.Strip(row))
					}
					if i > 0 && !strings.HasPrefix(ansi.Strip(row), "  ") {
						t.Errorf("row %d escaped group indentation: %q", i, ansi.Strip(row))
					}
				}
				compact := strings.Join(strings.Fields(ansi.Strip(strings.Join(rows, ""))), "")
				if !strings.Contains(compact, "internal/router/long_activity_filename.go") {
					t.Errorf("wrapped operation lost its path: %q", compact)
				}
			}
		})
	}
}

func TestEditGroupHeaderUsesShellHighlighting(t *testing.T) {
	p := activityui.Painter{}
	for _, source := range []string{"git stash push", "git stash push -m 'save edits'", "python3", "apply_patch", "echo `pwd` > out.txt"} {
		t.Run(source, func(t *testing.T) {
			blocks := activityui.GroupOperations([]activityui.Block{
				{Kind: "op", Verb: "Edit", Label: "`a.go` +2 -1 · " + source, EditSource: source, EditHeader: true},
			})
			rows := p.Block(blocks[0], 100)
			want := "• " + strings.Join(p.Highlight("bash", source), " ")
			if len(rows) < 2 || rows[0] != want || ansi.Strip(rows[0]) != "• "+source {
				t.Fatalf("header = %q, want Bash-highlighted %q", rows, want)
			}
			if rows[0] == ansi.Strip(rows[0]) {
				t.Fatalf("header has no syntax colors: %q", rows[0])
			}
		})
	}
	block := activityui.GroupOperations([]activityui.Block{{Kind: "op", Verb: "Read", Label: "`a.go`"}})[0]
	if got := p.Block(block, 100)[0]; got != "• Explored" {
		t.Fatalf("exploration heading was syntax highlighted: %q", got)
	}
}

func TestGroupReasoningOnlyMergesAdjacentShortSummary(t *testing.T) {
	for _, body := range []string{"**Locating files**", "**Heading**\n\nFirst paragraph.\n\nSecond paragraph."} {
		for _, adjacent := range []bool{true, false} {
			input := []activityui.Block{{Source: 1, Kind: "summary", Body: body}}
			if !adjacent {
				input = append(input, activityui.Block{Kind: "op", Verb: "Run", Code: "pwd"})
			}
			input = append(input, activityui.Block{Source: 2, Kind: "reads", Verb: "Read", Reads: []activityui.Read{{Path: "a.go"}}})
			got := activityui.GroupOperations(input)
			want := adjacent && !strings.Contains(body, "\n")
			if got[0].GroupSummary != want || (got[len(got)-1].GroupReasoning != "") != want || input[0].GroupSummary {
				t.Fatalf("incorrect reasoning merge: %+v", got)
			}
			if again := activityui.GroupOperations(got); !reflect.DeepEqual(again, got) {
				t.Fatal("reasoning grouping is not idempotent")
			}
		}
	}
}
