package activity

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

func TestExtractCopyUnannotatedRows(t *testing.T) {
	for _, row := range []string{"", "plain 猫 text", "\x1b[11G\x1b[31mcolored\x1b[0m", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\"} {
		if got, spans := ExtractCopy(row); got != row || len(spans) != 0 {
			t.Fatalf("unannotated row changed: %q, %+v", got, spans)
		}
		if allocs := testing.AllocsPerRun(100, func() { ExtractCopy(row) }); allocs != 0 {
			t.Fatalf("unannotated row allocated: %g", allocs)
		}
	}
}

func TestExtractCopyReusedParser(t *testing.T) {
	// An annotation may exceed the pooled parser's data buffer. Extraction
	// uses the complete decoded sequence, not that buffer's truncated data.
	for _, source := range []string{"first", strings.Repeat("猫", 3000), "last"} {
		fragment := CopyFragment{Text: source, Width: 4}
		for _, prefix := range []string{"\x1b[31G", "\x1b[G", "猫 "} {
			wantColumn := map[string]int{"\x1b[31G": 30, "\x1b[G": 0, "猫 ": 3}[prefix]
			row := prefix + copyTag(fragment) + "text\x1b[0m"
			clean, spans := ExtractCopy(row)
			if clean != prefix+"text\x1b[0m" || len(spans) != 1 || spans[0].Column != wantColumn || spans[0].Text != source {
				t.Fatalf("parser reuse lost source or cursor: clean=%q spans=%d", clean, len(spans))
			}
		}
	}
}

func BenchmarkExtractCopy(b *testing.B) {
	for _, annotated := range []bool{false, true} {
		name, row := "plain", "\x1b[12G\x1b[32mterminal output 猫\x1b[0m"
		if annotated {
			name = "annotated"
			row = "\x1b[12G" + copyTag(CopyFragment{Text: "terminal output 猫", Width: 18}) + row
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ExtractCopy(row)
			}
		})
	}
}

func selectedMarkdown(t *testing.T, source string, width int) string {
	t.Helper()
	p := Painter{CopySource: true}
	var parts []CopyFragment
	for _, row := range p.Markdown(source, width) {
		clean, spans := ExtractCopy(row)
		if strings.Contains(clean, "mekugi-copy") {
			t.Fatal("private metadata leaked")
		}
		for _, span := range spans {
			if f, ok := span.Clip(0, width); ok {
				parts = append(parts, f)
			}
		}
	}
	return CopyText(parts)
}

func TestMarkdownSourceCopyTables(t *testing.T) {
	source := "Name | Value\n--- | ---:\n**alpha beta gamma** | `a|b`\n猫 | "
	want := "| Name | Value |\n| --- | ---: |\n| **alpha beta gamma** | `a\\|b` |\n| 猫 |  |"
	for _, width := range []int{4, 12, 24, 40, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			if got := selectedMarkdown(t, source, width); got != want {
				t.Fatalf("width %d: copy=%q want=%q", width, got, want)
			}
		})
	}
}

func TestMarkdownSourceCopyPartialCell(t *testing.T) {
	p := Painter{CopySource: true}
	rows := p.Markdown("A | B\n--- | ---\n**alpha** | SECRET", 50)
	for _, row := range rows {
		_, spans := ExtractCopy(row)
		for _, span := range spans {
			if span.Text == "alpha" {
				f, ok := span.Clip(span.Column+1, span.Column+4)
				if !ok || CopyText([]CopyFragment{f}) != "**lph**" {
					t.Fatalf("partial=%q", CopyText([]CopyFragment{f}))
				}
				return
			}
		}
	}
	t.Fatal("no annotated cell")
}

func TestMarkdownSourceCopyWhitespaceAndFormatting(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"```go\n\tx := 1\t  \n\n\t  y := 2\n```", "\tx := 1\t  \n\n\t  y := 2"},
		{"```go\n  x := 1  \n\n\n    y := 2\n```", "  x := 1  \n\n\n    y := 2"},
		{"**some long bold words**\nnext  \nline", "**some long bold words**\nnext  \nline"},
		{"[hello world](https://example.com)", "[hello world](https://example.com)"},
		{"Read /tmp/source.go:26 and `src/file name.go`.", "Read /tmp/source.go:26 and `src/file name.go`."},
		{"# Title\n- item wraps across rows", "# Title\n- item wraps across rows"},
	} {
		for _, width := range []int{8, 20, 80} {
			if got := selectedMarkdown(t, tc.source, width); got != tc.want {
				t.Errorf("width %d source %q: got %q want %q", width, tc.source, got, tc.want)
			}
		}
	}
}

func TestMarkdownCopyWideGraphemeAndProjection(t *testing.T) {
	p := Painter{CopySource: true}
	row := p.Markdown("**A猫👩‍💻B**", 30)[0]
	clean, spans := ExtractCopy("\x1b[11G│ " + row)
	if ansi.Strip(clean) != "\x1b[11G│ A猫👩‍💻B" && !strings.Contains(ansi.Strip(clean), "A猫👩‍💻B") {
		t.Fatal(clean)
	}
	if len(spans) != 1 || spans[0].Column != 12 {
		t.Fatalf("spans=%+v", spans)
	}
	f, ok := spans[0].Clip(14, 16)
	if !ok || CopyText([]CopyFragment{f}) != "**猫👩‍💻**" {
		t.Fatalf("wide=%q", CopyText([]CopyFragment{f}))
	}
}

func TestMarkdownSourceCopyRepeatedPartialRecordLabel(t *testing.T) {
	p := Painter{CopySource: true}
	rows := p.Markdown("LongHeader | Other\n--- | ---\nx | y\nz | w", 12)
	var fragments []CopyFragment
	first := true
	for _, row := range rows {
		_, spans := ExtractCopy(row)
		for _, span := range spans {
			if span.Row == 0 && span.Cell == 0 && !span.Rule {
				left := span.Column
				if first {
					left += 2
					first = false
				}
				if f, ok := span.Clip(left, span.Column+span.Width); ok {
					fragments = append(fragments, f)
				}
			}
		}
	}
	if got := CopyText(fragments); got != "LongHeader" {
		t.Fatalf("selected repeated label=%q", got)
	}
}

func TestMarkdownSourceCopyNarrowIndentedList(t *testing.T) {
	for _, width := range []int{6, 8, 12, 30} {
		if got := selectedMarkdown(t, "    - alpha beta", width); got != "    - alpha beta" {
			t.Fatalf("width %d list=%q", width, got)
		}
	}
	p := Painter{CopySource: true}
	for _, row := range p.Markdown("    - alpha", 8) {
		_, spans := ExtractCopy(row)
		for _, span := range spans {
			if span.Text == "alpha" {
				f, ok := span.Clip(span.Column+1, span.Column+4)
				if !ok || CopyText([]CopyFragment{f}) != "    - lph" {
					t.Fatalf("partial list=%q", CopyText([]CopyFragment{f}))
				}
				return
			}
		}
	}
	t.Fatal("no narrow list body")
}

func TestMarkdownSourceCopyDistinctIdenticalCodeBlocks(t *testing.T) {
	for _, width := range []int{8, 30} {
		source := "```\nx\n```\n```\nx\n```"
		if got := selectedMarkdown(t, source, width); got != "x\nx" {
			t.Fatalf("identical blocks collapsed: %q", got)
		}
	}
}

func TestMarkdownSourceMetadataDoesNotChangeLayout(t *testing.T) {
	source := "    - **alpha beta**\n\nName | Value\n--- | ---\nx | `y`\n\n```\n\\tcode\n```\n\n> > A | B\n> > --- | ---\n> > x | y"
	for _, width := range []int{8, 30, 80} {
		var reference []string
		for _, p := range []Painter{{}, {CopySource: true}, {CopySource: true, LayoutOnly: true}} {
			rows := p.Markdown(source, width)
			for i, row := range rows {
				rows[i], _ = ExtractCopy(row)
				rows[i] = ansi.Strip(rows[i])
			}
			if reference == nil {
				reference = rows
			} else if strings.Join(rows, "\n") != strings.Join(reference, "\n") {
				t.Fatalf("source annotation changed layout at %d", width)
			}
		}
	}
}

func TestMarkdownSourceCopyInlineBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, source, want, content string
		left, right                 int
		kind                        ast.NodeKind
	}{
		{"code boundary backticks", "before `` `x` `` after", "`` `x` ``", "`x`", 7, 10, ast.KindCodeSpan},
		{"partial code backtick", "before `` `x` `` after", "`` `x ``", "`x", 7, 9, ast.KindCodeSpan},
		{"partial code space", "`x y`", "`  y `", " y", 1, 3, ast.KindCodeSpan},
		{"code only space", "`x  y`", "`  `", "  ", 1, 3, ast.KindCodeSpan},
		{"bold leading space", "**alpha beta**", " **beta**", "beta", 5, 10, ast.KindEmphasis},
		{"bold trailing space", "**alpha beta**", "**alpha** ", "alpha", 0, 6, ast.KindEmphasis},
		{"bold only space", "**alpha beta**", " ", "", 5, 6, ast.KindEmphasis},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Painter{CopySource: true}
			_, spans := ExtractCopy(p.Markdown(tc.source, 80)[0])
			f, ok := spans[0].Clip(tc.left, tc.right)
			if !ok {
				t.Fatal("selection omitted")
			}
			got := CopyText([]CopyFragment{f})
			if got != tc.want {
				t.Fatalf("copy=%q want=%q", got, tc.want)
			}
			// Parse copied Markdown independently: balanced-looking strings alone
			// cannot establish that formatting and code content survive copying.
			source := []byte(got)
			doc := goldmark.DefaultParser().Parse(text.NewReader(source))
			var content []string
			ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
				if entering && n.Kind() == tc.kind {
					content = append(content, parsedInlineText(n, source))
				}
				return ast.WalkContinue, nil
			})
			if tc.content == "" && len(content) != 0 || tc.content != "" && (len(content) != 1 || content[0] != tc.content) {
				t.Fatalf("parsed content=%q want=%q", content, tc.content)
			}
		})
	}
}

func TestMarkdownSourceCopyQuotedTables(t *testing.T) {
	for _, depth := range []int{1, 2} {
		prefix := strings.Repeat("> ", depth)
		source := prefix + "A | B\n" + prefix + "--- | ---\n" + prefix + "**alpha beta** | `x`"
		want := prefix + "| A | B |\n" + prefix + "| --- | --- |\n" + prefix + "| **alpha beta** | `x` |"
		for _, width := range []int{12, 24, 80} {
			got := selectedMarkdown(t, source, width)
			if got != want {
				t.Fatalf("depth %d width %d copy=%q want=%q", depth, width, got, want)
			}
			md := goldmark.New(goldmark.WithExtensions(extension.Table))
			doc := md.Parser().Parse(text.NewReader([]byte(got)))
			n := doc.FirstChild()
			for range depth {
				if n == nil || n.Kind() != ast.KindBlockquote {
					t.Fatal("copied table lost quote nesting")
				}
				n = n.FirstChild()
			}
			if n == nil || n.Kind().String() != "Table" {
				t.Fatal("copied quoted content is not a table")
			}
		}
	}
}

func TestMarkdownSourceCopyDistinctInlineSpans(t *testing.T) {
	for _, source := range []string{"`a` `` `b` ``", "`alpha` `beta`", "**alpha** **beta**", "[alpha](https://example.com) [beta](https://example.com)"} {
		for _, width := range []int{4, 8, 80} {
			got := selectedMarkdown(t, source, width)
			if got != source {
				t.Fatalf("width %d distinct styles merged: got %q want %q", width, got, source)
			}
		}
	}
	got := []byte(selectedMarkdown(t, "`a` `` `b` ``", 80))
	doc := goldmark.DefaultParser().Parse(text.NewReader(got))
	n := doc.FirstChild().FirstChild()
	if n == nil || n.Kind() != ast.KindCodeSpan || parsedInlineText(n, got) != "a" {
		t.Fatal("first code span changed")
	}
	n = n.NextSibling()
	if n == nil || n.Kind() != ast.KindText || parsedInlineText(n, got) != " " {
		t.Fatal("intervening plain-text space changed")
	}
	n = n.NextSibling()
	if n == nil || n.Kind() != ast.KindCodeSpan || parsedInlineText(n, got) != "`b`" || n.NextSibling() != nil {
		t.Fatal("second code span changed or merged")
	}
}

func parsedInlineText(node ast.Node, source []byte) string {
	var out strings.Builder
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			switch n := n.(type) {
			case *ast.Text:
				out.Write(n.Value(source))
			case *ast.String:
				out.Write(n.Value)
			}
		}
		return ast.WalkContinue, nil
	})
	return out.String()
}
