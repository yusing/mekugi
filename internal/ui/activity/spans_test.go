package activity

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestLayoutSpans(t *testing.T) {
	for _, token := range []string{"[Image 1]", "$review", `@"file name.go"`, "界é"} {
		for _, width := range []int{4, 16} {
			text := "prefix " + token + " tail"
			rows, points := LayoutSpans(text, []TextSpan{{Start: 7, End: 7 + len(token)}}, width)
			var plain []string
			for _, row := range rows {
				plain = append(plain, ansi.Strip(row))
				if ansi.StringWidth(row) > width {
					t.Fatalf("overwide row: %q", row)
				}
			}
			if strings.Join(plain, "") != text {
				t.Fatalf("lost content: %q", plain)
			}
			if ansi.StringWidth(token) <= width && !strings.Contains(strings.Join(plain, "\n"), token) {
				t.Fatalf("split fitting token %q: %q", token, plain)
			}
			for _, point := range points {
				if point.Offset == 7 && point.Column+ansi.StringWidth(token) > width && ansi.StringWidth(token) <= width {
					t.Fatalf("caret and token layout disagree: %+v", point)
				}
			}
		}
	}
}

func TestLayoutSpanLookalikeIsLiteral(t *testing.T) {
	rows, _ := LayoutSpans("prefix [Image 1]", nil, 12)
	if strings.Contains(strings.Join(rows, "\n"), "[Image 1]") || strings.Contains(strings.Join(rows, ""), "\x1b[") {
		t.Fatalf("literal lookalike became a token: %q", rows)
	}
}

func TestUISnapshotTokenKinds(t *testing.T) {
	var rows []string
	for _, tc := range []struct {
		text string
		kind TokenKind
	}{{"[Image 1]", ImageToken}, {"path.go", FileToken}, {"$review", SkillToken}} {
		text := "before " + tc.text + " after"
		rendered, _ := LayoutSpans(text, []TextSpan{{Start: 7, End: 7 + len(tc.text), Kind: tc.kind}}, 30)
		rows = append(rows, rendered...)
	}
	rows = append(rows, "plain after tokens")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/token_kinds.txt", rows, 30)
}

func TestAttachmentPathUsesSharedFormatting(t *testing.T) {
	var p Painter
	path := "/work/my `file`.go"
	rows := p.Block(Block{Kind: "op", Verb: "Attached", Path: path}, 100)
	if !strings.Contains(rows[0], Path(path)) || strings.Contains(ansi.Strip(rows[0]), `"`) {
		t.Fatalf("not a literal, styled path: %q", rows)
	}
}

func TestLayoutSpansExplicitNewlineAtWidth(t *testing.T) {
	rows, _ := LayoutSpans("abcdefghijklmn\n[Image 1]", []TextSpan{{Start: 15, End: 24}}, 14)
	if len(rows) != 2 || ansi.Strip(rows[1]) != "[Image 1]" {
		t.Fatalf("extra row at explicit newline: %q", rows)
	}
}
