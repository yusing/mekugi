package activity

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
)

func TestMarkdownClosingFenceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		close string
		valid bool
	}{{"````", true}, {"``` \t", true}, {"   ```", true}, {"``", false}, {"~~~", false}, {"```go", false}, {"    ```", false}, {"\t```", false}} {
		t.Run(strconv.Quote(tc.close), func(t *testing.T) {
			p := Painter{}
			body := "```text\ncode\n" + tc.close
			painted := ansi.Strip(strings.Join(p.Markdown(body+"\n**AFTER**", 24, false), "\n"))
			if strings.Contains(painted, "**AFTER**") == tc.valid {
				t.Fatalf("closing=%v: %q", tc.valid, painted)
			}
			paragraphs := activitymodel.Paragraphs("Run\n" + body + "\n\nRead `next.go`")
			if (len(paragraphs) == 2) != tc.valid {
				t.Fatalf("paragraphs=%q", paragraphs)
			}
			block := activitymodel.ParseOperation("Run\n" + body)
			if (block.Code == "code") != tc.valid {
				t.Fatalf("code=%q", block.Code)
			}
			sections := ReasoningSections(body + "\n# AFTER\n\nA paragraph.")
			if (len(sections) == 2 && sections[1].Label == "AFTER") != tc.valid {
				t.Fatalf("sections=%+v", sections)
			}
		})
	}
	p := Painter{}
	if got := ansi.Strip(p.Summary([]Block{{Kind: "answer", Body: "```mermaid\n   ```` \t\nAFTER"}}, 40)); strings.Contains(got, "```") || strings.Contains(got, "AFTER") {
		t.Fatalf("empty Mermaid summary includes closing fence or prose: %q", got)
	}
}
