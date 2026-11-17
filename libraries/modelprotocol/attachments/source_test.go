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

func TestSourceBoundaries(t *testing.T) {
	for _, body := range []string{"", "short", "é\r\n", "literal\n</mekugi-source-v1>\n" + sourceFrame("nested literal")} {
		header, footer := "reader: exit_code=0\n", "\ndiagnostics\n"+sourceFrame("diagnostic literal")
		frame := func(body string) string {
			return SourceRanges(header+body+footer, []SourceRange{{len(header), len(header) + len(body)}})
		}
		prefix := "instructions\n" + sourceFrame("quoted source example") + "\n"
		original := prefix + frame(body)
		calls := 0
		got := ProjectSources(original, func(source string) string {
			calls++
			if source != body {
				t.Fatalf("supplied bytes changed: %q, want %q", source, body)
			}
			return "See O1"
		})
		if calls != 1 || got != prefix+frame("See O1") {
			t.Fatalf("source framing changed prose: %q (%d calls)", got, calls)
		}
	}
}

func TestMalformedSourceDoesNotPublishEarlierBodies(t *testing.T) {
	for _, frame := range []string{
		strings.Replace(sourceFrame("body"), `bytes="4"`, `bytes="3"`, 1),
		strings.Replace(sourceFrame("body"), `bytes="4"`, `bytes="04"`, 1),
		strings.Replace(sourceFrame("body"), `bytes="4"`, `bytes="-1"`, 1),
		strings.Replace(sourceFrame("body"), `bytes="4"`, `bytes="99999999999999999999"`, 1),
		strings.TrimSuffix(sourceFrame("body"), ">"),
		"inline " + sourceFrame("body"), sourceFrame("body") + "inline",
		strings.Replace(sourceFrame("body"), `bodies="0:4"`, `bodies="0:3,2:4"`, 1),
		strings.Replace(sourceFrame("é"), `bodies="0:2"`, `bodies="0:1"`, 1),
	} {
		original := sourceFrame("earlier") + "\n" + frame
		if got := ProjectSources(original, func(string) string { t.Fatal("malformed input seeded a source"); return "" }); got != original {
			t.Fatal("malformed input changed")
		}
	}
}

func sourceFrame(body string) string {
	return SourceRanges(body, []SourceRange{{0, len(body)}})
}
