package activity_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestGroupOperationsGroupsOnlyEdits(t *testing.T) {
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
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("operations other than edits were grouped: %#v", got)
	}
	p := activityui.Painter{}
	var rows []string
	for _, block := range got {
		rows = append(rows, p.Block(block, 100)...)
	}
	want := []string{"Read   a.go", "Search needle (3 results)", "List   internal", "Inspect a.go", "Ran    go test ./...", "Read   b.go"}
	if got := plainLines(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("operation rows = %q, want %q", got, want)
	}
}

func TestGroupOperationsEditInvocationsShareVerbCell(t *testing.T) {
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
				if block.GroupHeader != "Edited" || block.GroupStart != (i == 0) || block.Source != input[i].Source {
					t.Fatalf("block %d group = %#v", i, block)
				}
				if i == 0 && block.GroupCount != 2 {
					t.Fatalf("group counts %d invocations, want 2", block.GroupCount)
				}
				rows = append(rows, p.Block(block, 100)...)
			}
			suffix := " via " + source + " ×2"
			if source == "apply_patch" {
				suffix = " ×2"
			}
			// Rows share a verb cell as wide as the group's widest verb.
			want := []string{
				"Edited  a.go  +3 -1 ━━━━━━━━" + suffix,
				"Created b.go  +2    ━━━━━━━━",
				"Deleted c.go  -4    ━━━━━━━━ · exit 2",
			}
			if got := plainLines(rows); !reflect.DeepEqual(got, want) {
				t.Fatalf("rows = %q, want %q", got, want)
			}
		})
	}
	// Later Edit rows leave the shared verb cell blank.
	p := activityui.Painter{}
	var rows []string
	for _, block := range activityui.GroupOperations([]activityui.Block{
		{Kind: "op", Verb: "Edit", Label: "`a.go` +1 -0 · python3", EditSource: "python3", EditHeader: true},
		{Kind: "op", Verb: "Edit", Label: "`b.go` +2 -0 · python3", EditSource: "python3"},
	}) {
		rows = append(rows, p.Block(block, 100)...)
	}
	if got := plainLines(rows); !strings.HasPrefix(got[0], "Edited a.go") || !strings.HasPrefix(got[1], "       b.go") {
		t.Fatalf("rows = %q", got)
	}
}

func TestEditGroupsSplitBySourceAndOutcome(t *testing.T) {
	blocks := activityui.GroupOperations([]activityui.Block{
		{Source: 1, Kind: "op", Verb: "Edit", Label: "`a.go` · +1 −0 · apply_patch", EditSource: "apply_patch", EditHeader: true},
		{Source: 2, Kind: "op", Verb: "Edit", Label: "`a.go` +1 -0 · python3", EditSource: "python3", EditHeader: true},
		{Source: 3, Kind: "op", Verb: "Edit", Label: "`a.go` · +1 −0 · failed · apply_patch", EditSource: "apply_patch", EditHeader: true},
		{Source: 4, Kind: "op", Verb: "Edit", Label: "`a.go` · cat (requested)", EditSource: "cat (requested)", EditHeader: true},
	})
	want := []string{"Edited", "Edited", "Edit failed", "Edit requested"}
	for i, block := range blocks {
		if block.GroupHeader != want[i] || !block.GroupStart || block.GroupCount != 1 {
			t.Errorf("block %d group = %q start=%t count=%d", i, block.GroupHeader, block.GroupStart, block.GroupCount)
		}
	}

}

func TestEditRowsAlignCounts(t *testing.T) {
	p := activityui.Painter{}
	blocks := activityui.GroupOperations([]activityui.Block{
		{Kind: "op", Verb: "Edit", Label: "`doc/a.md` · +7 −0 · apply_patch", EditSource: "apply_patch", EditHeader: true},
		{Kind: "op", Verb: "Edit", Label: "`internal/router/long_name.go` · +54 −2 · apply_patch", EditSource: "apply_patch"},
	})
	var columns []int
	for _, block := range blocks {
		rows := p.Block(block, 80)
		plain := ansi.Strip(rows[0])
		columns = append(columns, strings.Index(plain, "+"))
	}
	if columns[0] < 0 || columns[0] != columns[1] {
		t.Fatalf("count columns = %v", columns)
	}
	narrow := p.Block(blocks[1], 40)
	for _, row := range narrow {
		if ansi.StringWidth(row) > 40 {
			t.Fatalf("aligned row exceeds width: %q", ansi.Strip(row))
		}
	}
}

func TestEditRowsShareCountColumnWhenTight(t *testing.T) {
	p := activityui.Painter{}
	paths := []string{"README.md", "doc/spec/native_ui.md", "internal/router/app_server_resume_codex_e2e_test.go", "internal/router/app_server_ui.go"}
	counts := []int{46, 337, 97, 919}
	var input []activityui.Block
	for i, path := range paths {
		input = append(input, activityui.Block{Kind: "op", Verb: "Edit", Label: fmt.Sprintf("`%s` +%d -0 · python3", path, counts[i]), EditSource: "python3", EditHeader: i == 0})
	}
	blocks := activityui.GroupOperations(input)
	// Widths where the shortest counts fit the widest path's column but the
	// widest counts do not, and where the column must narrow.
	for _, width := range []int{68, 69, 60} {
		columns := map[int]bool{}
		for _, block := range blocks {
			rows := p.Block(block, width)
			for _, row := range rows {
				if ansi.StringWidth(row) > width {
					t.Fatalf("row exceeds width %d: %q", width, ansi.Strip(row))
				}
			}
			plain := ansi.Strip(rows[0])
			columns[ansi.StringWidth(plain[:strings.Index(plain, "+")])] = true
		}
		if len(columns) != 1 {
			t.Fatalf("count columns at width %d = %v", width, columns)
		}
	}
}

func TestEditGroupLongSourceWrapping(t *testing.T) {
	p := activityui.Painter{}
	source := "git stash push -m 'save pending changes before rebase'"
	for _, width := range []int{24, 40} {
		blocks := activityui.GroupOperations([]activityui.Block{
			{Kind: "op", Verb: "Edit", Label: "`a.go` +2 -1 · " + source, EditSource: source, EditHeader: true},
			{Kind: "op", Verb: "Edit", Label: "`b.go` +1 -1 · " + source, EditSource: source},
		})
		rows := p.Block(blocks[0], width)
		for _, row := range rows {
			if ansi.StringWidth(row) > width {
				t.Fatalf("file row exceeds width %d: %q", width, row)
			}
		}
		plain := strings.Join(strings.Fields(ansi.Strip(strings.Join(rows, " "))), " ")
		if !strings.Contains(plain, "Edited a.go +2 -1") || !strings.Contains(plain, "via "+source) {
			t.Fatalf("wrapped group lost content at width %d: %q", width, plain)
		}
	}
}

func TestOperationGroupWrapping(t *testing.T) {
	for _, width := range []int{24, 40} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			// Wide enough rows shorten the path's middle rather than wrap it.
			wraps := width < 30
			p := activityui.Painter{}
			blocks := activityui.GroupOperations([]activityui.Block{
				{Kind: "op", Verb: "Edit", Label: "`internal/router/long_activity_filename.go` +13 -11 · python3", EditSource: "python3", EditHeader: true},
				{Kind: "op", Verb: "Edit", Label: "`a.go` +1 -1 · python3", EditSource: "python3"},
				{Kind: "reads", Verb: "Read", Reads: []activityui.Read{{Path: "internal/router/long_activity_filename.go", Ranges: []string{"1:100"}}}},
				{Kind: "reads", Verb: "Read", Reads: []activityui.Read{{Path: "a.go"}}},
			})
			for _, block := range []activityui.Block{blocks[0], blocks[2]} {
				rows := p.Block(block, width)
				// A source that does not fit follows on its own row.
				if wraps && len(rows) < 2 || !wraps && (len(rows) > 2 || len(rows) == 2 && strings.TrimSpace(ansi.Strip(rows[1])) != "via python3") {
					t.Fatalf("unexpected operation layout at width %d: %q", width, plainLines(rows))
				}
				for i, row := range rows {
					if ansi.StringWidth(row) > width {
						t.Errorf("row %d exceeds width %d: %q", i, width, ansi.Strip(row))
					}
					if i > 0 && !strings.HasPrefix(ansi.Strip(row), "       ") {
						t.Errorf("row %d escaped its verb column: %q", i, ansi.Strip(row))
					}
				}
				compact := strings.Join(strings.Fields(ansi.Strip(strings.Join(rows, ""))), "")
				if wraps && !strings.Contains(compact, "internal/router/long_activity_filename.go") ||
					!wraps && (!strings.Contains(compact, "…/long_acti") || !strings.Contains(compact, "name.go")) {
					t.Errorf("operation lost its path: %q", compact)
				}
			}
		})
	}
}

func TestEditGroupSourceUsesShellHighlighting(t *testing.T) {
	p := activityui.Painter{}
	for _, source := range []string{"git stash push", "git stash push -m 'save edits'", "python3", "echo `pwd` > out.txt", "cat, python3"} {
		t.Run(source, func(t *testing.T) {
			blocks := activityui.GroupOperations([]activityui.Block{
				{Kind: "op", Verb: "Edit", Label: "`a.go` +2 -1 · " + source, EditSource: source, EditHeader: true},
				{Kind: "op", Verb: "Edit", Label: "`b.go` +1 -1 · " + source, EditSource: source},
			})
			rows := p.Block(blocks[0], 100)
			if len(rows) != 1 || !strings.HasSuffix(ansi.Strip(rows[0]), " via "+source) {
				t.Fatalf("first row = %q", plainLines(rows))
			}
			// Each source of a multi-source edit is its own command.
			for part := range strings.SplitSeq(source, ", ") {
				if highlighted := strings.Join(p.Highlight("bash", part), " "); !strings.Contains(rows[0], highlighted) || highlighted == part {
					t.Fatalf("row %q lacks Bash-highlighted %q", rows[0], highlighted)
				}
			}
			if later := plainLines(p.Block(blocks[1], 100)); strings.Contains(later[0], source) {
				t.Fatalf("later row repeats the source: %q", later)
			}
		})
	}
	requested := activityui.GroupOperations([]activityui.Block{
		{Kind: "op", Verb: "Edit", Label: "`a.go` · cat (requested)", EditSource: "cat (requested)", EditHeader: true},
		{Kind: "op", Verb: "Edit", Label: "`b.go` · cat (requested)", EditSource: "cat (requested)"},
	})[0]
	rows := p.Block(requested, 100)
	if ansi.Strip(rows[0]) != "Edit   a.go via cat · requested" || strings.Contains(strings.Join(rows, ""), "requested)") {
		t.Fatalf("requested edit row = %q", rows)
	}
}

func TestRanRowWrapsAtShellWords(t *testing.T) {
	p := activityui.Painter{}
	command := "go test ./internal/router -run 'Native Patch|CodeMode' -count=1"
	block := activityui.Block{Kind: "op", Verb: "Run", Code: command, Lang: "bash", Fenced: true, ExitCode: 1,
		Tail: []string{"--- FAIL: TestX", "FAIL"}, TailOmitted: 3}
	rows := p.Block(block, 44)
	plain := plainLines(rows)
	for _, row := range rows {
		if ansi.StringWidth(row) > 44 {
			t.Fatalf("row exceeds width: %q", ansi.Strip(row))
		}
	}
	if !strings.HasPrefix(plain[0], "Ran    go test") || !strings.Contains(rows[0], activityui.Red+"\x1b[1mRan") {
		t.Fatalf("failed command row = %q", plain)
	}
	var joined []string
	for _, row := range plain {
		if strings.HasPrefix(row, "       ┆") || strings.HasPrefix(row, "       · exit") {
			continue
		}
		row = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(row, "Ran"), " \\"))
		joined = append(joined, row)
	}
	if got := strings.Join(joined, " "); got != command {
		t.Fatalf("wrapped command = %q from %q", got, plain)
	}
	for _, row := range plain {
		// The quoted pattern holds a blank, which is not a break point.
		if strings.HasSuffix(row, "'Native \\") {
			t.Fatalf("broke inside quotes: %q", plain)
		}
	}
	tail := strings.Join(plain, "\n")
	for _, want := range []string{"       · exit 1", "       ┆ … 3 earlier lines", "       ┆ --- FAIL: TestX", "       ┆ FAIL"} {
		if !strings.Contains(tail, want) {
			t.Fatalf("missing %q:\n%s", want, tail)
		}
	}
}

func TestRanRowPutsStatementsOnRows(t *testing.T) {
	p := activityui.Painter{}
	command := `git status --short; git show --format=fuller 20ba742c && cat "a b.md" || true; find . -exec rm {} \; ; echo $(a; b)`
	want := []string{
		"Ran    git status --short;",
		"       git show --format=fuller 20ba742c &&",
		`       cat "a b.md" ||`,
		"       true;",
		`       find . -exec rm {} \; ;`,
		"       echo $(a; b)",
	}
	if plain := plainLines(p.Block(activityui.Block{Kind: "op", Verb: "Run", Code: command, Lang: "bash", Fenced: true}, 60)); !reflect.DeepEqual(plain, want) {
		t.Fatalf("statement rows = %q", plain)
	}
	if short := plainLines(p.Block(activityui.Block{Kind: "op", Verb: "Run", Code: "git diff --cc", Lang: "bash", Fenced: true}, 60)); len(short) != 1 {
		t.Fatalf("a command that fits was wrapped: %q", short)
	}
}

func TestFitPathKeepsFileName(t *testing.T) {
	p := activityui.Painter{}
	long := "/tmp/codex-go-quality/37a43aab6e2751ad1c16ab071dfd2df9160db3e5ed0eedcf/reports/a7c95cd1cbde870158cbe777f6e8bf8db071e4291.diff"
	for _, path := range []string{long, "/tmp/codex-go-quality/37a43aab6e2751ad1c16ab071dfd2df9160db3e5ed0eedcf/reports/r.diff"} {
		block := activityui.Block{Kind: "reads", Verb: "Read", Reads: []activityui.Read{{Path: path, Ranges: []string{"1:9"}}}}
		rows := p.Block(block, 60)
		plain := ansi.Strip(rows[0])
		if len(rows) != 1 || ansi.StringWidth(rows[0]) > 60 || !strings.Contains(plain, "…") || !strings.HasSuffix(plain, ".diff L1–9") {
			t.Fatalf("fitted read = %q", plainLines(rows))
		}
	}
}

func TestOperationRowsNameTheirOutcome(t *testing.T) {
	p := activityui.Painter{}
	for _, tc := range []struct {
		name   string
		blocks []activityui.Block
		want   []string
	}{
		{"ran", []activityui.Block{{Kind: "op", Verb: "Run", Code: "git diff --cc", Lang: "bash", Fenced: true},
			{Kind: "filter", Body: "~tokens 1.2K→700"}}, []string{"Ran    git diff --cc", "       ~tokens 1.2K→700"}},
		{"failed", []activityui.Block{{Kind: "op", Verb: "Run", Code: "false", Lang: "bash", Fenced: true, ExitCode: 1, Tail: []string{"boom"}}},
			[]string{"Ran    false · exit 1", "       ┆ boom"}},
		{"failed program", []activityui.Block{{Kind: "op", Verb: "Run", Code: "print(1)\nprint(2)", Lang: "python", Fenced: true, ExitCode: 3}},
			[]string{"Ran    │ print(1)", "       │ print(2)", "       · exit 3"}},
		{"search", []activityui.Block{{Kind: "op", Verb: "Search", Label: "`^test` in `Makefile`"}}, []string{"Search ^test in Makefile"}},
		{"edited", []activityui.Block{{Kind: "op", Verb: "Edit", Label: "`a.go` +4 -2 · python3", EditSource: "python3", EditHeader: true}},
			[]string{"Edited a.go +4 -2 via python3"}},
		{"created", []activityui.Block{{Kind: "op", Verb: "Create", Label: "`b.go` +6 -0", EditSource: "apply_patch", EditHeader: true}},
			[]string{"Created b.go +6"}},
		{"failed edit", []activityui.Block{
			{Kind: "op", Verb: "Edit", Label: "`c.go` +1 -1 · failed", EditSource: "apply_patch", EditHeader: true},
			{Kind: "op", Verb: "Edit", Label: "`d.go` +1 -0 · failed", EditSource: "apply_patch"},
		}, []string{"Edit   c.go  +1 -1 · failed", "       d.go  +1"}},
		{"requested", []activityui.Block{{Kind: "op", Verb: "Edit", Label: "`a.go` · cat (requested)", EditSource: "cat (requested)", EditHeader: true}},
			[]string{"Edit   a.go via cat · requested"}},
		// Reasoning stays its own row, which heads the operations after it.
		{"reasoning", []activityui.Block{{Kind: "summary", Body: "**Checking**"}, {Kind: "op", Verb: "Read", Label: "`Makefile`"}},
			[]string{"• Checking", "Read   Makefile"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rows []string
			for _, block := range activityui.GroupOperations(tc.blocks) {
				rows = append(rows, p.Block(block, 80)...)
			}
			if got := plainLines(rows); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rows = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFailedRanRowKeepsExitAfterWrappedCommand(t *testing.T) {
	p := activityui.Painter{}
	block := activityui.Block{Kind: "op", Verb: "Run", Code: "go test ./internal/router -count=1", Lang: "bash", Fenced: true, ExitCode: 2}
	got := plainLines(p.Block(block, 30))
	if got[len(got)-1] != "         -count=1 · exit 2" {
		t.Fatalf("wrapped failure = %q", got)
	}
	for _, row := range p.Block(block, 30) {
		if ansi.StringWidth(row) > 30 {
			t.Fatalf("row exceeds width: %q", ansi.Strip(row))
		}
	}
}

func TestAlignVerbsPadsOnlyToAdjacentVerbs(t *testing.T) {
	p := activityui.Painter{}
	blocks := activityui.AlignVerbs([]activityui.Block{
		{Kind: "op", Verb: "Run", Code: "git status", Lang: "bash", Fenced: true},
		{Kind: "filter", Body: "~tokens 1.2K→700"},
		{Kind: "op", Verb: "Run JavaScript", Label: "`x()`"},
		{Kind: "summary", Body: "**Checking**"},
		{Kind: "op", Verb: "Read", Label: "`a.go`"},
		{Kind: "op", Verb: "Search", Label: "`needle`"},
	})
	var rows []string
	for _, block := range blocks {
		rows = append(rows, p.Block(block, 80)...)
	}
	// A verb wider than the default column does not widen its neighbors'.
	want := []string{"Ran git status", "    ~tokens 1.2K→700", "Run JavaScript x()", "• Checking", "Read   a.go", "Search needle"}
	if got := plainLines(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q, want %q", got, want)
	}
}

func TestRanRowBoundsSourceRows(t *testing.T) {
	p := activityui.Painter{}
	// A python3 -c program built with JSON.stringify: one line of literal \n escapes.
	oneLine := "python3 -c " + strings.Repeat(`"import json\nfrom pathlib import Path\nprint(1)\n" `, 20)
	program := strings.Repeat("print('row')\n", 30) + "print('last')"
	for _, tc := range []struct {
		name  string
		block activityui.Block
		hint  string
	}{
		{"one line", activityui.Block{Kind: "op", Verb: "Run", Code: oneLine, Lang: "bash", Fenced: true, ExitCode: 1, Tail: []string{"SyntaxError"}}, "       … +"},
		{"program", activityui.Block{Kind: "op", Verb: "Run", Code: program, Lang: "python", Fenced: true}, "       │ … +"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			whole := plainLines(p.Block(tc.block, 60))
			tc.block.SourceRows = 8
			plain := plainLines(p.Block(tc.block, 60))
			source := plain
			if tc.block.ExitCode != 0 {
				// The exit follows the hint; the failure output follows the source.
				source = plain[:len(plain)-1]
				if !strings.HasSuffix(source[len(source)-1], "· exit 1") || !strings.HasSuffix(plain[len(plain)-1], "SyntaxError") {
					t.Fatalf("exit or output lost after clipping: %q", plain)
				}
			}
			if len(source) != 8 || !strings.HasPrefix(source[7], tc.hint) {
				t.Fatalf("clipped rows = %q", plain)
			}
			omitted := len(whole) - len(plain) + 1
			if !strings.Contains(source[7], activityui.MoreLines(omitted)) {
				t.Fatalf("hint %q does not count the %d omitted rows", source[7], omitted)
			}
		})
	}
	short := activityui.Block{Kind: "op", Verb: "Run", Code: "git diff --cc", Lang: "bash", Fenced: true, SourceRows: 8}
	if plain := plainLines(p.Block(short, 60)); len(plain) != 1 {
		t.Fatalf("a command within its rows was clipped: %q", plain)
	}
}

func TestRanRowExpandsSourceTabs(t *testing.T) {
	p := activityui.Painter{}
	block := activityui.Block{Kind: "op", Verb: "Run", Code: "print('a')\nsub['msymbol\t' + x] += 1", Lang: "python", Fenced: true}
	for _, row := range p.Block(block, 40) {
		if strings.Contains(row, "\t") {
			t.Fatalf("raw tab reached a painted row: %q", row)
		}
	}
}
