package compact

import (
	"strings"
	"testing"
)

func TestReloadMetadataDisplay(t *testing.T) {
	block := CompactMetadataStart + `{"sources":[{"changes":["blue1..3"]}]}` + CompactMetadataEnd
	for _, tc := range []struct{ text, visible string }{
		{"Done\n" + block, "Done\n"},
		{"Done\n" + CompactMetadataStart + "{bad}", "Done\n"},
		{"Done\n<mekugi-reload-", "Done\n"},
		{"Done\n" + CompactMetadataStart + "{bad}" + CompactMetadataEnd, "Done\n"},
		{"Example: `" + block + "`", "Example: `" + block + "`"},
		{"```\n" + block + "\n```", "```\n" + block + "\n```"},
		{"> " + block, "> " + block},
		{"    " + block, "    " + block},
		{"Done\n" + block + "\nMore text", "Done\n" + block + "\nMore text"},
		{"Ordinary 世界\n<aside>text</aside>", "Ordinary 世界\n<aside>text</aside>"},
	} {
		if got := ReloadText(tc.text); got != tc.visible {
			t.Fatalf("display %q, want %q", got, tc.visible)
		}
		for split := 0; split <= len(tc.text); split++ {
			var stream ReloadStream
			got := stream.Push(tc.text[:split]) + stream.Push(tc.text[split:])
			if got != strings.TrimSuffix(tc.visible, "\n") && got != tc.visible {
				t.Fatalf("split %d streamed %q, want %q", split, got, tc.visible)
			}
		}
	}
	var stream ReloadStream
	chunk := strings.Repeat("ordinary answer ", 1000)
	for range 100 {
		if stream.Push(chunk) != chunk || stream.pending != "" {
			t.Fatal("ordinary streaming accumulated answer text")
		}
	}
}
