package activity

import (
	"strings"
	"testing"
)

func TestReasoningSections(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []Block
	}{
		{
			name: "multiple bold titles",
			text: "  **Inspecting the renderer**\n\nThe first paragraph.\n\nMore detail.\n\n**Checking timing**\n\nThe second paragraph.\n  ",
			want: []Block{
				{Kind: "summary", Label: "Inspecting the renderer", Body: "**Inspecting the renderer**\n\nThe first paragraph.\n\nMore detail."},
				{Kind: "summary", Label: "Checking timing", Body: "**Checking timing**\n\nThe second paragraph."},
			},
		},
		{
			name: "markdown headings without blank separators",
			text: "# Inspecting\nFirst paragraph.\n## Checking\nSecond paragraph.",
			want: []Block{
				{Kind: "summary", Label: "Inspecting", Body: "# Inspecting\nFirst paragraph."},
				{Kind: "summary", Label: "Checking", Body: "## Checking\nSecond paragraph."},
			},
		},
		{
			name: "plain titles and paragraphs",
			text: "Short title\n\nFirst paragraph.\n\nSecond paragraph.\n\nShort title two\n\nThird paragraph.",
			want: []Block{
				{Kind: "summary", Label: "Short title", Body: "Short title\n\nFirst paragraph.\n\nSecond paragraph."},
				{Kind: "summary", Label: "Short title two", Body: "Short title two\n\nThird paragraph."},
			},
		},
		{
			name: "untitled introduction before explicit title",
			text: "An introductory sentence.\n\n**Checking**\n\nA detailed sentence.",
			want: []Block{
				{Kind: "summary", Body: "An introductory sentence."},
				{Kind: "summary", Label: "Checking", Body: "**Checking**\n\nA detailed sentence."},
			},
		},
		{
			name: "heading only bold",
			text: " \n**Checking tests**\n\n",
			want: []Block{{Kind: "summary", Label: "Checking tests", Body: "**Checking tests**"}},
		},
		{
			name: "heading only markdown",
			text: "\n### Checking tests\n",
			want: []Block{{Kind: "summary", Label: "Checking tests", Body: "### Checking tests"}},
		},
		{
			name: "hash in title is content",
			text: "# Checking C#\n\nA paragraph.",
			want: []Block{{Kind: "summary", Label: "Checking C#", Body: "# Checking C#\n\nA paragraph."}},
		},
		{
			name: "optional closing heading hashes",
			text: "## Checking tests ##\n\nA paragraph.",
			want: []Block{{Kind: "summary", Label: "Checking tests", Body: "## Checking tests ##\n\nA paragraph."}},
		},
		{
			name: "headings inside backtick fence",
			text: "**Real title**\n\n```markdown\n# Not a title\n**Not a title either**\n\nPlain fake title\n\nA code paragraph.\n```\n\nA real paragraph.",
			want: []Block{{Kind: "summary", Label: "Real title", Body: "**Real title**\n\n```markdown\n# Not a title\n**Not a title either**\n\nPlain fake title\n\nA code paragraph.\n```\n\nA real paragraph."}},
		},
		{
			name: "headings inside tilde fence and section after it",
			text: "~~~markdown\n# Not a title\n**Also code**\n~~~\n\n# Real title\n\nA paragraph.",
			want: []Block{
				{Kind: "summary", Body: "~~~markdown\n# Not a title\n**Also code**\n~~~"},
				{Kind: "summary", Label: "Real title", Body: "# Real title\n\nA paragraph."},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertReasoningSections(t, tc.text, tc.want)
		})
	}
}

func TestReasoningSectionsPlainTitleBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		title string
	}{
		{"sixty display columns", strings.Repeat("a", 60)},
		{"wide characters at sixty columns", strings.Repeat("界", 30)},
		{"eight words", "One two three four five six seven eight"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.title + "\n\nA paragraph."
			assertReasoningSections(t, body, []Block{{Kind: "summary", Label: tc.title, Body: body}})
		})
	}
}

func TestReasoningSectionsDoesNotSplitProse(t *testing.T) {
	texts := []struct {
		name string
		text string
	}{
		{"sentence paragraphs", "I am checking the renderer.\n\nThe timing needs another look.\n\nIs this correct?"},
		{"exclamation", "Checking now!\n\nA paragraph."},
		{"colon", "Checking timing:\n\nA paragraph."},
		{"semicolon", "Checking timing;\n\nA paragraph."},
		{"over sixty columns", strings.Repeat("a", 61) + "\n\nA paragraph."},
		{"wide characters exceed sixty columns", strings.Repeat("界", 31) + "\n\nA paragraph."},
		{"over eight words", "One two three four five six seven eight nine\n\nA paragraph."},
		{"no blank line after candidate", "Checking timing\nA paragraph."},
		{"no blank line before candidate", "A paragraph.\nChecking timing\n\nAnother paragraph."},
		{"bullet", "- Checking timing\n\nA paragraph."},
		{"numbered list", "1. Checking timing\n\nA paragraph."},
		{"table", "| Checking | timing |\n\nA paragraph."},
		{"indented code", "A paragraph.\n\n    Checking timing\n\nAnother paragraph."},
		{"inline bold", "**Checking** the timing.\n\nA paragraph."},
		{"inline hash", "#not-a-heading\n\nA paragraph."},
	}
	for _, tc := range texts {
		t.Run(tc.name, func(t *testing.T) {
			assertReasoningSections(t, tc.text, []Block{{Kind: "summary", Body: tc.text}})
		})
	}
}

func assertReasoningSections(t *testing.T, text string, want []Block) {
	t.Helper()
	got := ReasoningSections(text)
	if len(got) != len(want) {
		t.Fatalf("ReasoningSections(%q) returned %d blocks, want %d: %+v", text, len(got), len(want), got)
	}
	for i, block := range got {
		if block.Section != i {
			t.Errorf("section ordinal = %d, want %d", block.Section, i)
		}
		if block.Kind != want[i].Kind || block.Label != want[i].Label || block.Body != want[i].Body {
			t.Errorf("block %d = {Kind: %q, Label: %q, Body: %q}, want {Kind: %q, Label: %q, Body: %q}", i, block.Kind, block.Label, block.Body, want[i].Kind, want[i].Label, want[i].Body)
		}
	}
}
