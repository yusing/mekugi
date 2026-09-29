package activity

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

// Tree journal results carry no author heading: the answer is Markdown, then
// the change report. The report must still paint as structured rows.
func TestJournalTreeResultPaintsChangeReport(t *testing.T) {
	text := livediff.Safe("Updated the assigned tests.\n\n- ● /1 Port tests · 2m3s\n\n**Changes:** apple14..apple18\n\n"+
		"Aggregated numstat (this agent's recorded evaluations, not a net diff):\n\n"+
		"    M\t1\t1\tinternal/router/app_server_events_test.go\n    M\t15\t28\tinternal/router/app_server_preview_test.go\n"+
		"\nCumulative: 18 recorded evaluations across 18 retained changes.\n", false)
	journal, ok := ParseJournal(text)
	if !ok || len(journal.Groups) != 1 || len(journal.Stats) != 2 || journal.Changes != "apple14..apple18" {
		t.Fatalf("tree result did not parse: %+v %v", journal, ok)
	}
	if answer := journal.Groups[0].Answers[0].Text; !strings.HasPrefix(answer, "Updated the assigned tests.") || !strings.Contains(answer, "/1 Port tests") || strings.Contains(answer, "Changes") {
		t.Fatalf("answer lost its rows or kept the report: %q", answer)
	}
	p := Painter{}
	card := ansi.Strip(strings.Join(p.Event(Block{Kind: "final", Body: text, Journal: journal}, 80), "\n"))
	for _, want := range []string{"✓ answer", "Changes apple14..apple18", "+15 -28", "app_server_preview_test.go", "Cumulative: 18"} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q:\n%s", want, card)
		}
	}
	for _, unwanted := range []string{"Aggregated numstat", "**", "Journal result"} {
		if strings.Contains(card, unwanted) {
			t.Fatalf("card kept %q:\n%s", unwanted, card)
		}
	}
}

func TestJournalResultRecognition(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		ok, empty  bool
	}{
		{"retained heading", "Journal result `/root/a`\n\n- `amber`\n\n  Answer", true, false},
		{"retained empty heading", "Journal result `/root/a`\nNo new journal entries.\n\n**Changes:**\nNo recorded changes.\n", true, true},
		{"empty", "No new journal entries.\n\n**Changes:**\nNo recorded changes.\n", true, true},
		{"unavailable", "Done.\n\n**Changes:**\nChanges unavailable: storage is unavailable.\n", true, false},
		{"authored report", "Summary\n\n**Changes:** none\n\nI changed nothing else.", false, false},
		{"authored heading only", "Done. Fixed the bug.\n\n**Changes:** updated foo.go to handle nil input.", false, false},
		{"authored indented code", "Summary\n\n**Changes:**\n\n    func main() {}", false, false},
		{"code-span bullets", "- `internal/a.go`\n- `internal/b.go`\n\n**Changes:**\nNo recorded changes.\n", true, false},
		{"plain answer", "Just an answer.", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal, ok := ParseJournal(tc.text)
			if ok != tc.ok || ok && (len(journal.Groups) == 0) != tc.empty {
				t.Fatalf("ParseJournal(%q) = %+v, %v", tc.text, journal, ok)
			}
		})
	}
}

func TestJournalHeadlessCodeSpanBulletsStayMarkdown(t *testing.T) {
	journal, ok := ParseJournal("- `internal/a.go`\n- `internal/b.go`\n\n**Changes:**\nNo recorded changes.\n")
	if !ok || len(journal.Groups) != 1 || len(journal.Groups[0].Answers) != 1 || journal.Groups[0].Answers[0].ID != "" || !strings.Contains(journal.Groups[0].Answers[0].Text, "internal/b.go") {
		t.Fatalf("headless bullets parsed as v1 items: %+v", journal)
	}
}
