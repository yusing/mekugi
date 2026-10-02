package activity

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestErrorPreview(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"first_nonempty", "\n \t\n  first failure  \nprivate diagnostic", "first failure"},
		{"controls", "\x1b[31mfailed\x1b[0m\x07\r\trequest\x1b]0;injected title\x07\nsecond", "failed    request"},
		{"empty", "\n\t\x1b[2J\n", ""},
		{"exact_limit", strings.Repeat("a", 240), strings.Repeat("a", 240)},
		{"ascii_clipped", strings.Repeat("a", 241), strings.Repeat("a", 240) + "…"},
		{"partial_rune", strings.Repeat("a", 239) + "界extra", strings.Repeat("a", 239) + "…"},
		{"complete_rune", strings.Repeat("a", 237) + "界extra", strings.Repeat("a", 237) + "界…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ErrorPreview(tc.body)
			if got != tc.want {
				t.Fatalf("preview = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("preview is not valid UTF-8: %q", got)
			}
		})
	}
}

func TestErrorDetailsHover(t *testing.T) {
	plain := ErrorRows(Block{Kind: "error", Body: "failed\nAdditional diagnostics"}, 40)
	hovered := ErrorRows(Block{Kind: "error", Body: "failed\nAdditional diagnostics", Hovered: true}, 40)
	if strings.Join(plain, "\n") == strings.Join(hovered, "\n") {
		t.Fatal("hover does not decorate the details target")
	}
	if !strings.Contains(hovered[len(hovered)-1], "\x1b[4m") {
		t.Fatal("hovered details target is not underlined")
	}
	if ansi.Strip(strings.Join(plain, "\n")) != ansi.Strip(strings.Join(hovered, "\n")) {
		t.Fatal("hover changed the preview or details text")
	}
}

func TestErrorDialogPreservesFullLiteralBody(t *testing.T) {
	body := "\x1b[31m**Failure**\x1b[0m\x07\n\n" + strings.Repeat("x", 300) + "\n```sh\nprintf '<details>'\n```\nlate-search-needle\tend\x1b]0;injected\x07"
	want := "**Failure**\n\n" + strings.Repeat("x", 300) + "\n```sh\nprintf '<details>'\n```\nlate-search-needle    end"
	p := Painter{Theme: livediff.DarkTheme}
	for _, width := range []int{16, 60} {
		page := p.DialogPage(Block{Kind: "error", Body: body}, width)
		if page.Text != want {
			t.Fatalf("width %d: copied body = %q, want %q", width, page.Text, want)
		}
		if ansi.Strip(page.Title) != "Error" || page.Live {
			t.Fatalf("width %d: error page metadata = %+v", width, page)
		}
		var rendered strings.Builder
		for _, line := range page.Lines {
			rendered.WriteString(ansi.Strip(line.Text))
		}
		// Search consumes the complete laid-out lines, not the inline preview.
		for _, literal := range []string{"**Failure**", strings.Repeat("x", 300), "```sh", "printf", "'<details>'", "late-search-needle"} {
			if !strings.Contains(rendered.String(), literal) {
				t.Errorf("width %d: full dialog lost literal %q", width, literal)
			}
		}
	}
}

func TestErrorDialogLogicalSearchRows(t *testing.T) {
	p := Painter{Theme: livediff.DarkTheme}
	page := p.DialogPage(Block{Kind: "error", Body: "界aaNeedle crosses a visual boundary\nsecond line"}, 4)
	if len(page.Lines) != 2 || !page.Lines[0].Wrap {
		t.Fatal("error lines were split before search")
	}
	if row, found := page.MatchRow(0, 4, "NEEDLE crosses"); !found || row != 1 {
		t.Fatalf("cross-wrap match row = %d, found %v, want 1", row, found)
	}
	if _, found := page.MatchRow(0, 4, "missing"); found {
		t.Fatal("missing query matched")
	}
	for i := range page.Lines {
		if got, want := page.RowCount(i, 4), len(page.Rows(i, 4)); got != want {
			t.Fatalf("line %d row count %d, actual %d", i, got, want)
		}
	}
}

func TestUISnapshotActivityErrorInline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		width   int
		body    string
		hovered bool
	}{
		{"complete", 48, "Request failed: permission denied", true},
		{"wrapped_complete", 20, "Request failed: permission denied", true},
		{"multiline", 48, "\nRequest failed: permission denied\nSensitive diagnostic remains in details\nAnother detail", false},
		{"long", 48, "Request failed: " + strings.Repeat("diagnostic ", 30) + "\nFinal diagnostic", false},
		{"hovered", 48, "Request failed: permission denied\nFull diagnostic", true},
		{"narrow", 20, "Request failed: permission denied\nFull diagnostic", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Painter{Theme: livediff.DarkTheme}
			rows := p.Block(Block{Kind: "error", Body: tc.body, Hovered: tc.hovered}, tc.width)
			uisnapshot.Assert(t, "testdata/snapshots/activity_error_inline_"+tc.name+".txt", strings.Join(rows, "\n")+"\n")
		})
	}
}

func TestUISnapshotActivityErrorDialog(t *testing.T) {
	body := "**Request failed**\n\nProvider rejected the request.\n```json\n{\"error\": \"permission denied\"}\n```\nDetails: token lacks scope."
	for _, tc := range []struct {
		name          string
		width, height int
	}{
		{"full", 64, 15},
		{"narrow", 24, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Painter{Theme: livediff.DarkTheme}
			page := p.DialogPage(Block{Kind: "error", Body: body}, tc.width-4)
			var rows []string
			for i := range page.Lines {
				rows = append(rows, page.Rows(i, tc.width-4)...)
			}
			frame := DialogFrame{Page: page, Rows: rows, Total: len(rows), Footer: "esc close · ↑↓ scroll · y copy"}
			uisnapshot.Assert(t, "testdata/snapshots/activity_error_dialog_"+tc.name+".txt", strings.Join(p.Dialog(frame, tc.width, tc.height), "\n")+"\n")
		})
	}
}
