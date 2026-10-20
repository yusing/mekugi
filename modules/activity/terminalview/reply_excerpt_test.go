package terminalview

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

func TestNativeAssignmentExcerptWrapsAndRetainsTarget(t *testing.T) {
	v := NewView()
	var out conversationLines
	v.replyContext(&out, presentation.Entry{Facts: presentation.Facts{Seq: 42, Kind: "assignment", Assignment: true, AssignmentText: "Review the response and verify the assignment excerpt wraps without losing its navigation target."}}, "│ ", 36)
	v.replyExcerpt(&out, presentation.Entry{Facts: presentation.Facts{Seq: 43}}, "The response excerpt remains visible.", "│ ", "│ ", 36, conversationLatestRows)
	if len(out.lines) < 5 || !strings.HasPrefix(ansi.Strip(out.lines[1]), "│ ▎ Review") || !strings.HasPrefix(ansi.Strip(out.lines[2]), "│ ▎ ") {
		t.Fatalf("assignment was not separately quoted and wrapped: %q", out.lines)
	}
	if !strings.HasSuffix(ansi.Strip(out.lines[2]), "…") || strings.TrimSpace(ansi.Strip(out.lines[2])) == "│ ▎ …" {
		t.Fatalf("assignment ellipsis must end the last excerpt line: %q", out.lines)
	}
	for i, line := range out.lines {
		if ansi.StringWidth(line) > 38 {
			t.Fatalf("row %d overflows: %q", i, line)
		}
		if i < 3 && out.questions[i] != 42 {
			t.Fatalf("assignment row %d lost its target", i)
		}
	}
	if !strings.Contains(ansi.Strip(strings.Join(out.lines, "\n")), "The response excerpt") || out.questions[len(out.questions)-1] != 43 {
		t.Fatal("response excerpt or its Activity target was lost")
	}
}

func TestNativeReplyExcerptEllipsisStaysInline(t *testing.T) {
	v := NewView()
	var out conversationLines
	v.replyExcerpt(&out, presentation.Entry{Facts: presentation.Facts{Seq: 43}}, "First response line.\nSecond response line.\nThird response line.", "│ ", "│ ", 24, conversationEarlierRows)
	if len(out.lines) != 3 || ansi.Strip(out.lines[1]) != "│ Second response line.…" {
		t.Fatalf("response ellipsis did not stay inline: %q", out.lines)
	}
	for _, line := range out.lines {
		if ansi.StringWidth(line) > 26 {
			t.Fatalf("excerpt overflow: %q", line)
		}
	}
}

func TestNativeExcerptEllipsisSkipsParagraphGaps(t *testing.T) {
	v := NewView()
	var out conversationLines
	v.replyContext(&out, presentation.Entry{Facts: presentation.Facts{Seq: 42, Kind: "assignment", Assignment: true, AssignmentText: "Fix the finding.\n\nThen rerun the tests.\n\nReport back."}}, "", 40)
	v.replyExcerpt(&out, presentation.Entry{Facts: presentation.Facts{Seq: 43}}, "APPROVE. No remaining actionable findings.\n\nVerified with make test.\n\nDone.", "", "", 60, conversationEarlierRows)
	got := make([]string, len(out.lines))
	for i, line := range out.lines {
		got[i] = ansi.Strip(line)
	}
	want := []string{
		got[0],
		"▎ Fix the finding.",
		"▎ Then rerun the tests.…",
		"APPROVE. No remaining actionable findings.",
		"Verified with make test.…",
		"↩ Open reply · +1 line",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("excerpt rows:\n got %q\nwant %q", got, want)
	}
}
