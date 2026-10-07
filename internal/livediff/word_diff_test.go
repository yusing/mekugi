package livediff

import (
	"strconv"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

// Read terminal styles at painted characters, including wrapped carry SGRs.
func emphasizedText(text, background string) string {
	var style, wanted uv.Style
	var out strings.Builder
	parser := ansi.NewParser()
	parser.SetHandler(ansi.Handler{HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
		if cmd == 'm' {
			uv.ReadStyle(params, &style)
		}
	}, Print: func(r rune) {
		background := uv.Style{Bg: style.Bg}
		if background.Equal(&wanted) {
			out.WriteRune(r)
		}
	}})
	parser.Parse([]byte(background))
	wanted.Bg = style.Bg
	style = uv.Style{}
	parser.Parse([]byte(text))
	return out.String()
}

func TestWordDiffReplacementBlocks(t *testing.T) {
	for _, tc := range []struct{ name, old, next, removed, added string }{
		{"code", "return old_name(41)\n", "return new_name(42)\n", "old_name41", "new_name42"},
		{"insertion", "keep the words\n", "keep all the words\n", "", "all "},
		{"reflow", "keep these words\nthen tail\n", "keep these\nnew words then tail\n", "", "new "},
		{"unicode operator", "é界 <= 1\n", "é界 >= 1\n", "<", ">"},
		{"addition", "", "new file\n", "", ""},
		{"removal", "removed line\nkeep\n", "keep\n", "", ""},
		{"bounded", strings.Repeat("old ", 1100) + "\n", strings.Repeat("next ", 1100) + "\n", "", ""},
	} {
		const theme = DarkTheme
		t.Run(tc.name, func(t *testing.T) {
			review := mekugi.RenderReviewFile("file.go", "file.go", tc.old, tc.next)
			hunks, err := review.Hunks()
			if err != nil {
				t.Fatal(err)
			}
			var removed, added strings.Builder
			var renderer Renderer
			for _, hunk := range hunks {
				before, after, err := renderer.ColorHunk(t.Context(), theme, review, hunk.Rows)
				if err != nil {
					t.Fatal(err)
				}
				rows := append([]mekugi.ReviewRow(nil), hunk.Rows...)
				AlignHunkColors(rows, before, after)
				for i, row := range rows {
					if ansi.Strip(row.Text) != Safe(strings.TrimSuffix(hunk.Rows[i].Text, "\n"), false) {
						t.Fatal("word colors changed source text")
					}
					for _, kind := range []byte{'-', '+'} {
						marked := emphasizedText(row.Text, theme.WordBackground(kind))
						if row.Kind != kind && marked != "" {
							t.Fatal("emphasis escaped its side")
						}
						if kind == '-' {
							removed.WriteString(marked)
						} else {
							added.WriteString(marked)
						}
					}
				}
				plain, err := renderer.ColorSource(t.Context(), theme, "file.go", tc.next)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(strings.Join(plain, ""), "\x1b[48;") {
					t.Fatal("word colors contaminated source cache")
				}
			}
			if removed.String() != tc.removed || added.String() != tc.added {
				t.Fatalf("emphasis = %q -> %q, want %q -> %q", removed.String(), added.String(), tc.removed, tc.added)
			}
		})
	}
}

func TestWordDiffGraphemeGeometry(t *testing.T) {
	for _, pair := range [][2]string{{"👍🏻", "👍🏽"}, {"👩‍💻", "👩‍🔬"}, {"🇹🇼", "🇹🇭"}} {
		prefix := strings.Repeat("x", 72)
		review := mekugi.RenderReviewFile("emoji.unknown", "emoji.unknown", prefix+pair[0]+"\n", prefix+pair[1]+"\n")
		files := []File{{Path: "emoji.unknown", Chunks: []Chunk{{Review: review}}}}
		for _, width := range []int{78, 80} {
			want, err := new(Renderer).Render(t.Context(), DarkTheme, files, "", width)
			if err != nil {
				t.Fatal(err)
			}
			renderer := Renderer{LayoutOnly: true}
			got, err := renderer.Render(t.Context(), DarkTheme, files, "", width)
			if err != nil {
				t.Fatal(err)
			}
			assertViewportGeometry(t, got, want)
			if err := got.PaintViewport(t.Context(), 0, len(got.Lines)); err != nil {
				t.Fatal(err)
			}
			assertViewportGeometry(t, got, want)
			var marked strings.Builder
			for _, line := range got.Lines {
				marked.WriteString(emphasizedText(line, DarkTheme.WordBackground('+')))
			}
			if marked.String() != pair[1] {
				t.Fatalf("split or lost grapheme emphasis: %q", marked.String())
			}
		}
	}
}

func TestUISnapshotWordDiffWrappedSourceStyles(t *testing.T) {
	const old, next = "return old_identifier\n", "return very_long_new_identifier\n"
	review := mekugi.RenderReviewFile("file.go", "file.go", old, next)
	for _, theme := range []Theme{TerminalTheme, DarkTheme, LightTheme} {
		render, err := new(Renderer).Render(t.Context(), theme, []File{{Path: "file.go", Chunks: []Chunk{{Review: review}}}}, "", 16)
		if err != nil {
			t.Fatal(err)
		}
		var removed, added strings.Builder
		for _, line := range render.Lines {
			removed.WriteString(emphasizedText(line, theme.WordBackground('-')))
			added.WriteString(emphasizedText(line, theme.WordBackground('+')))
		}
		if removed.String() != "old_identifier" || added.String() != "very_long_new_identifier" {
			t.Fatalf("wrapped emphasis included gutters/padding or lost carry: %q -> %q", removed.String(), added.String())
		}
		rows := append(render.Lines, "plain after diff")
		uisnapshot.AssertTerminal(t, "testdata/snapshots/wrapped_source_styles_"+strconv.Itoa(int(theme))+".txt", rows, 16)
	}
}
