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
		{"plain", "A short acknowledgement", ""},
		{"preamble", "Introduction\n" + valid, ""},
		{"case", strings.Replace(valid, "## Summary", "## summary", 1), ""},
		{"trailing space", strings.Replace(valid, "## Summary", "## Summary ", 1), ""},
		{"empty summary", "## Summary\n \t\n## Details\nEvidence", ""},
		{"empty details", "## Summary\nOutcome\n## Details\n \t", ""},
		{"repeated summary", "## Summary\nOutcome\n## Summary\nOther\n## Details\nEvidence", ""},
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
