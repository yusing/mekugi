package presentation

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/textsanitize"
)

func TestChildReplySummaryInterpretation(t *testing.T) {
	const summary = "Fixed navigation. Focused tests pass; live resume is unverified."
	const valid = "## Summary\n" + summary + "\n\n## Details\nEvidence and references."
	for _, tc := range []struct{ name, body, want string }{
		{"valid", valid, summary},
		{"leading blanks", "\n \t\n" + valid, summary},
		{"CRLF", strings.ReplaceAll(valid, "\n", "\r\n"), summary},
		{"later headings ordinary", valid + "\n## Summary\nMore detail\n## Details\nStill detail", summary},
		{"chosen section", "## Summary\n" + summary + "\n\n### Evidence\nTests.", summary},
		{"top heading", "## Summary\n" + summary + "\n# Findings\nTests.", summary},
		{"summary only", "## Summary\n" + summary, ""},
		{"hashtag", "## Summary\nOutcome\n#tag\n## Notes\nEvidence", "Outcome\n#tag"},
		{"plain", "A short acknowledgement", ""},
		{"preamble", "Introduction\n" + valid, ""},
		{"case", strings.Replace(valid, "## Summary", "## summary", 1), ""},
		{"trailing space", strings.Replace(valid, "## Summary", "## Summary ", 1), ""},
		{"empty summary", "## Summary\n \t\n## Details\nEvidence", ""},
		{"empty details", "## Summary\nOutcome\n## Details\n \t", ""},
		{"partial", "## Summary\nOutcome\n## Det", ""},
		{"quoted", "> " + strings.ReplaceAll(valid, "\n", "\n> "), ""},
		{"indented", "    " + strings.ReplaceAll(valid, "\n", "\n    "), ""},
		{"fenced", "```markdown\n" + valid + "\n```", ""},
		{"backticks inside summary", "## Summary\n```md\n## Summary\n## Details\n```\nResult\n## Details\nEvidence", "```md\n## Summary\n## Details\n```\nResult"},
		{"tildes inside summary", "## Summary\n  ~~~md\n## Details\n  ~~~\nResult\n## Details\nEvidence", "  ~~~md\n## Details\n  ~~~\nResult"},
		{"quoted details only", "## Summary\nOutcome\n> ## Details\nEvidence", ""},
		{"indented details only", "## Summary\nOutcome\n    ## Details\nEvidence", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, kind := range []string{"reply", "final"} {
				entry := ItemContent{Seq: 17, Agent: "/root/worker", Kind: kind, Text: tc.body, ItemComplete: true}
				if kind == "reply" {
					entry.Message = &DirectedContent{From: entry.Agent, To: "/root", Text: tc.body}
				}
				values := entry.Values()
				if len(values) != 1 || strings.Trim(values[0].ReplySummary, "\r\n") != tc.want || values[0].Source != 17 {
					t.Fatalf("%s values = %#v, want summary %q", kind, values, tc.want)
				}
				if tc.want != "" && (strings.TrimSpace(values[0].ReplyDetails) == "" || values[0].Body != textsanitize.MarkdownSource(tc.body)) {
					t.Fatalf("details or original body lost: %#v", values[0])
				}
				if tc.name == "valid" && values[0].ReplyDetails != "Evidence and references." || tc.name == "chosen section" && values[0].ReplyDetails != "### Evidence\nTests." {
					t.Fatalf("details = %q", values[0].ReplyDetails)
				}
				entry.ItemComplete = false
				if entry.Values()[0].ReplySummary != "" {
					t.Fatal("in-flight reply interpreted")
				}
				entry.ItemComplete, entry.Agent = true, "/root"
				if entry.Values()[0].ReplySummary != "" {
					t.Fatal("root answer interpreted")
				}
			}
		})
	}
}

func TestChildReplySummaryFallbackBody(t *testing.T) {
	for _, body := range []string{
		"## Summary\nFixed navigation.",
		"\n \t\n## Summary\r\nFixed navigation.\r\n\r\n- Evidence.\r\n**Decision needed:** choose a name.",
		"## Summary\nOutcome\n> ## Details\nQuoted evidence.",
		"## Summary\nOutcome\n    ## Details\nIndented evidence.",
		"## Summary\nOutcome\n```md\n## Details\n```\nEvidence.",
		"## Summary\nOutcome\n~~~md\n## Details\n~~~\nEvidence.",
		"## Summary\nOutcome\n## Notes\n \t",
	} {
		for _, kind := range []string{"reply", "final"} {
			entry := ItemContent{Seq: 17, Agent: "/root/worker", Kind: kind, Text: body, ItemComplete: true}
			if kind == "reply" {
				entry.Message = &DirectedContent{From: entry.Agent, To: "/root", Text: body}
			}
			_, want, _ := strings.Cut(body, "## Summary")
			value := entry.Values()[0]
			if value.ReplySummary != "" || value.ReplyDetails != textsanitize.MarkdownSource(strings.TrimPrefix(strings.TrimPrefix(want, "\r"), "\n")) || value.Body != textsanitize.MarkdownSource(body) {
				t.Fatalf("%s fallback = %#v", kind, value)
			}
			entry.ItemComplete = false
			if value := entry.Values()[0]; value.ReplyDetails != "" {
				t.Fatal("in-flight reply interpreted")
			}
			entry.ItemComplete, entry.Agent = true, "/root"
			if value := entry.Values()[0]; value.ReplyDetails != "" {
				t.Fatal("root answer interpreted")
			}
			if entry.Message != nil {
				entry.Agent, entry.Message.To = "/root/worker", "/root/other"
				if value := entry.Values()[0]; value.ReplyDetails != "" {
					t.Fatal("sibling-directed reply interpreted")
				}
			}
		}
	}
	for _, body := range []string{"## Summary\n\n", "## Summary\n \t"} {
		entry := ItemContent{Agent: "/root/worker", Kind: "final", Text: body, ItemComplete: true}
		if value := entry.Values()[0]; value.ReplySummary != "" || value.ReplyDetails != "" {
			t.Fatalf("empty structure interpreted: %#v", value)
		}
	}
}
