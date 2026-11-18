package attachments

import (
	"strconv"
	"strings"
	"testing"
)

func TestNativeSourceContentsKeepBodiesOpaque(t *testing.T) {
	const body = "literal\nSource \"@not-a-receipt\":\n</mekugi-source-v1>\n"
	header := "\nSource \"@file\":\n"
	failure := "\nSource \"$missing\": CONTENT NOT ATTACHED: " + strconv.Quote("unavailable\nSource \"@also-not-a-receipt\":") + "\n"
	text := SourceRanges(header+body+"\n"+failure, []SourceRange{{Start: len(header), End: len(header) + len(body)}})
	values := SourceContents("Source content (tool output).\n" + text)
	if len(values) != 2 || values[0].Reference != "@file" || values[0].Body != body || values[1].Reference != "$missing" || !strings.Contains(values[1].Error, "unavailable\nSource") {
		t.Fatalf("source receipts: %+v", values)
	}
	if got := SourceContents(strings.Replace(text, `bytes="`, `bytes="9`, 1)); len(got) != 0 {
		t.Fatalf("malformed envelope admitted receipts: %+v", got)
	}
	if got := SourceContents("Source \"@fake\":\ntext"); len(got) != 0 {
		t.Fatalf("unframed prose admitted receipts: %+v", got)
	}
}
