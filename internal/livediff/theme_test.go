package livediff

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
)

func testThemeChunk(key, path, diff string, applied bool) Chunk {
	return Chunk{
		Key: key, Applied: applied,
		Review: mekugi.ReviewFile{BeforePath: path, AfterPath: path, Diff: diff},
	}
}

func assertNoBackground(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "\x1b[48;") {
		t.Fatalf("unexpected background color: %q", text)
	}
}

func TestLiveDiffThemeTokens(t *testing.T) {
	for _, theme := range []Theme{TerminalTheme, LightTheme, DarkTheme} {
		t.Run(strconv.Itoa(int(theme)), func(t *testing.T) {
			const source = "func retainedShellTestOutput() {\n\tcount := len(make([]string, 42)) // comment\n\treturn \"value\"\n}\n"
			lines, err := new(Renderer).ColorSource(t.Context(), theme, "file.go", source)
			if err != nil {
				t.Fatal(err)
			}
			colored := strings.Join(lines, "\n") + "\n"
			if ansi.Strip(colored) != source {
				t.Fatalf("theme changed source: %q", colored)
			}
			for _, token := range []struct {
				text string
				kind chroma.TokenType
			}{
				{"func", chroma.KeywordDeclaration},
				{"retainedShellTestOutput", chroma.NameFunction},
				{"len", chroma.NameBuiltin},
				{"make", chroma.NameBuiltin},
				{"string", chroma.KeywordType},
				{":=", chroma.Operator},
				{"42", chroma.LiteralNumberInteger},
				{"\"value\"", chroma.LiteralString},
				{"// comment", chroma.CommentSingle},
			} {
				style := theme.Foreground(token.kind)
				if style == "" || !strings.Contains(colored, style+token.text+"\x1b[39m") {
					t.Errorf("missing %s style for %q: %q", token.kind, token.text, colored)
				}
			}
			assertNoBackground(t, colored)
			if strings.Contains(colored, "\x1b[0m") {
				t.Fatal("token styling should reset only foreground")
			}
		})
	}
}

func TestLiveDiffThemeLanguageCoverage(t *testing.T) {
	for _, theme := range []Theme{TerminalTheme, LightTheme, DarkTheme} {
		for _, tc := range []struct {
			path, source, name string
			kind               chroma.TokenType
		}{
			{"file.py", "def render(value):\n    return len(value)\n", "render", chroma.NameFunction},
			{"file.js", "function render(value) { return Math.abs(value); }\n", "Math", chroma.NameBuiltin},
			{"file.ts", "function render(value: number) { return Math.abs(value); }\n", "Math", chroma.NameBuiltin},
			{"file.html", "<div class=\"card\">Hello</div>\n", "div", chroma.NameTag},
			{"file.html", "<div class=\"card\">Hello</div>\n", "class", chroma.NameAttribute},
		} {
			lines, err := new(Renderer).ColorSource(t.Context(), theme, tc.path, tc.source)
			colored := strings.Join(lines, "\n") + "\n"
			if err != nil || ansi.Strip(colored) != tc.source ||
				!strings.Contains(colored, theme.Foreground(tc.kind)+tc.name+"\x1b[39m") {
				t.Errorf("theme %d, %s: missing %s color or changed source: %q, %v", theme, tc.path, tc.kind, colored, err)
			}
		}
	}
}

func TestLiveDiffThemeGeometryAndFallback(t *testing.T) {
	chunk := testThemeChunk("edit", "file.go", "@@ -1 +1 @@\n-return \"界\"\n+return len(\"界\")\n", true)
	chunk.Highlighted = true
	file := File{Path: "file.go", Highlighted: true, Chunks: []Chunk{chunk}}
	for _, width := range []int{1, 13, 90} {
		var baseline string
		for _, theme := range []Theme{TerminalTheme, LightTheme, DarkTheme} {
			render, err := new(Renderer).Render(t.Context(), theme, []File{file}, "", width, 0, chunk)
			if err != nil {
				t.Fatal(err)
			}
			output := strings.Join(render.Lines, "\n")
			if theme == TerminalTheme {
				baseline = ansi.Strip(output)
			} else if ansi.Strip(output) != baseline {
				t.Fatalf("theme %d changed geometry at width %d: %q", theme, width, output)
			}
			for _, line := range render.Lines {
				if ansi.StringWidth(line) > width-1 || Safe(line, true) != line {
					t.Fatalf("theme %d produced unsafe or overflowing output: %q", theme, line)
				}
			}
		}
	}
	for _, theme := range []Theme{LightTheme, DarkTheme} {
		for _, path := range []string{"file.go", "unknown.extension"} {
			for _, source := range []string{"\"unfinished\n", "/* comment\n", "\n\n", strings.Repeat("x", MaxSyntaxBytes+1) + "\n"} {
				lines, err := new(Renderer).ColorSource(t.Context(), theme, path, source)
				if err != nil || ansi.Strip(strings.Join(lines, "\n"))+"\n" != source {
					t.Fatalf("theme %d changed incomplete/unsupported source for %s: %v", theme, path, err)
				}
			}
		}
	}
}

func TestLiveDiffBackgroundTheme(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  Theme
		ok    bool
	}{
		{"\x1b]11;rgb:f/f/f\a", LightTheme, true},
		{"\x1b]11;rgb:ff/FF/ff\x1b\\", LightTheme, true},
		{"\x1b]11;rgb:eee/eee/eee\a", LightTheme, true},
		{"\x1b]11;rgb:ffff/ffff/ffff\a", LightTheme, true},
		{"\x1b]11;rgb:0/0/0\a", DarkTheme, true},
		{"\x1b]11;rgb:1111/1111/1111\x1b\\", DarkTheme, true},
		{"\x1b]11;rgb:8000/8000/8000\a", LightTheme, true},
		{"\x1b]11;rgb:7fff/7fff/7fff\a", DarkTheme, true},
		{"\x1b]10;rgb:ff/ff/ff\a", TerminalTheme, false},
		{"\x1b]11;rgb:fffff/ffff/ffff\a", TerminalTheme, false},
		{"\x1b]11;rgb:gg/ff/ff\a", TerminalTheme, false},
		{"\x1b]11;rgb:+f/ff/ff\a", TerminalTheme, false},
		{"\x1b]11;rgb:/ff/ff\a", TerminalTheme, false},
		{"\x1b]11;rgb:ff/ff\a", TerminalTheme, false},
		{"\x1b]11;rgb:ff/ff/ff", TerminalTheme, false},
		{"", TerminalTheme, false},
	} {
		got, ok := BackgroundTheme(tc.reply)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q: got %d/%v, want %d/%v", tc.reply, got, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct {
		value string
		want  Theme
	}{
		{"0;15", LightTheme},
		{"0;default;7", LightTheme},
		{"15;0", DarkTheme},
		{"7;8", DarkTheme},
		{"", TerminalTheme},
		{"15", TerminalTheme},
		{"0;", TerminalTheme},
		{"0;blue", TerminalTheme},
		{"0;4", TerminalTheme},
		{"0;256", TerminalTheme},
	} {
		if got := EnvironmentTheme(tc.value); got != tc.want {
			t.Errorf("COLORFGBG %q: got %d, want %d", tc.value, got, tc.want)
		}
	}
}

func TestLiveDiffOSCReplyBound(t *testing.T) {
	for _, payload := range []string{"11;rgb:ff/ff/ff\a", "11;rgb:11/11/11\x1b\\", "0;qFnp\a", strings.Repeat("x", 1024) + "\x1b\\"} {
		var osc OSC
		osc.Consume(']')
		var reply string
		var complete bool
		for _, key := range []byte(payload) {
			reply, complete = osc.Consume(key)
			if len(osc.Reply) > 128 {
				t.Fatal("unbounded terminal response")
			}
		}
		if !complete || osc.Active || len(reply) > 129 {
			t.Fatalf("reply did not finish cleanly: %+v %q", osc, reply)
		}
	}
}

func TestLiveDiffThemeContrast(t *testing.T) {
	luminance := func(rgb [3]float64) float64 {
		for i, value := range rgb {
			value /= 255
			if value <= .04045 {
				rgb[i] = value / 12.92
			} else {
				rgb[i] = math.Pow((value+.055)/1.055, 2.4)
			}
		}
		return .2126*rgb[0] + .7152*rgb[1] + .0722*rgb[2]
	}
	for _, tc := range []struct {
		theme      Theme
		background [3]float64
	}{
		{LightTheme, [3]float64{255, 255, 255}},
		{LightTheme, [3]float64{247, 247, 247}},
		{DarkTheme, [3]float64{17, 17, 17}},
		{LightTheme, [3]float64{230, 245, 233}},
		{LightTheme, [3]float64{255, 235, 233}},
		{DarkTheme, [3]float64{22, 42, 29}},
		{DarkTheme, [3]float64{49, 27, 31}},
		{DarkTheme, [3]float64{36, 41, 46}},
	} {
		foregrounds := []string{tc.theme.Accent()}
		for _, kind := range []chroma.TokenType{
			chroma.Keyword, chroma.KeywordType, chroma.NameFunction, chroma.NameBuiltin,
			chroma.NameOther, chroma.LiteralString, chroma.LiteralNumber, chroma.Operator,
			chroma.Comment, chroma.GenericInserted, chroma.GenericDeleted,
		} {
			foregrounds = append(foregrounds, tc.theme.Foreground(kind))
		}
		for _, foreground := range foregrounds {
			parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(foreground, "\x1b[38;2;"), "m"), ";")
			if len(parts) != 3 {
				t.Fatalf("theme %d missing truecolor Foreground: %q", tc.theme, foreground)
			}
			var rgb [3]float64
			for i, part := range parts {
				value, err := strconv.ParseUint(part, 10, 8)
				if err != nil {
					t.Fatal(err)
				}
				rgb[i] = float64(value)
			}
			a, b := luminance(rgb), luminance(tc.background)
			if ratio := (max(a, b) + .05) / (min(a, b) + .05); ratio < 4.5 {
				t.Errorf("theme %d foreground %q on %v has contrast %.2f, want >= 4.5", tc.theme, foreground, tc.background, ratio)
			}
		}
	}
}

func TestLiveDiffRowFills(t *testing.T) {
	chunk := testThemeChunk("edit", "file.go",
		"@@ -9,3 +19,3 @@\n context\n-return \"old\"\n+return \"界\"\n tail\n", true)
	chunk.Status = ""
	for _, theme := range []Theme{TerminalTheme, LightTheme, DarkTheme} {
		for _, width := range []int{1, 2, 3, 8, 80} {
			render, err := new(Renderer).Render(t.Context(), theme, []File{{Path: "file.go", Chunks: []Chunk{chunk}}}, "", width, 0, chunk)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range render.Lines {
				if ansi.StringWidth(line) > width-1 {
					t.Fatalf("overflow: %q", line)
				}
				logical, exact := slices.BinarySearch(render.RowStarts, i)
				if !exact {
					logical--
				}
				sourceIndex := logical - (len(render.RowStarts) - 4)
				if sourceIndex != 1 && sourceIndex != 2 {
					assertNoBackground(t, line)
					continue
				}
				kind := byte('-')
				if sourceIndex == 2 {
					kind = '+'
				}
				if width > 3 {
					if !strings.Contains(line, theme.RowBackground(kind)) ||
						ansi.StringWidth(line) != width-1 ||
						!strings.HasSuffix(line, "\x1b[0m") {
						t.Fatalf("missing full-width bounded fill: %q", line)
					}
					if strings.Contains(line, "\x1b[39m") {
						t.Fatalf("token reset escaped row Foreground: %q", line)
					}
				}
			}
			if width == 80 {
				for i, prefix := range []string{"  19│ context", "  10│-return", "  20│+return", "  21│ tail"} {
					if !strings.HasPrefix(ansi.Strip(render.Lines[i+1]), prefix) {
						t.Fatalf("wrong single-column coordinate: %q", render.Lines[i+1])
					}
				}
			}
		}
	}
}

func TestLiveDiffPaletteVariety(t *testing.T) {
	for _, theme := range []Theme{TerminalTheme, LightTheme, DarkTheme} {
		colors := map[string]bool{}
		for _, kind := range []chroma.TokenType{chroma.Keyword, chroma.NameFunction, chroma.KeywordType, chroma.LiteralString, chroma.LiteralNumber, chroma.NameOther} {
			colors[theme.Foreground(kind)] = true
		}
		if len(colors) < 6 {
			t.Fatalf("theme %d collapsed syntax categories: %v", theme, colors)
		}
	}
}
