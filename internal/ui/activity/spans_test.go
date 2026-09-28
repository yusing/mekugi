package activity

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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

func TestTokenKindsUseDistinctColors(t *testing.T) {
	seen := make(map[string]bool)
	for _, kind := range []TokenKind{ImageToken, FileToken, SkillToken} {
		rows, _ := LayoutSpans("x", []TextSpan{{Start: 0, End: 1, Kind: kind}}, 10)
		style, _, _ := strings.Cut(rows[0], "x")
		if style == "" || seen[style] || strings.Contains(style, "[34m") || strings.Contains(style, "[1;34m") {
			t.Fatalf("missing, duplicate, or blue token style: %q", style)
		}
		seen[style] = true
	}
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
